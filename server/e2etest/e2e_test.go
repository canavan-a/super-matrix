// Package e2etest runs the whole server in-process (TLS off) with a real
// relay and real targets, and drives it over HTTP the way the app and
// visitors do.
package e2etest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"main/api"
	"main/config"
	"main/database"
	"main/edge"
	"main/registry"
	"main/tunnel"
)

const rootPW = "correct horse battery"

type env struct {
	t      *testing.T
	addr   string // host:port of the server
	token  string // admin session
	client *http.Client
	// e2eRelayToken is the token of the last relay startRelay made.
	e2eRelayToken string
	srv           *api.API
	edge          *edge.Edge
	dir           string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		AdminHost: "127.0.0.1", BaseDomain: "test", TLS: "off", DataDir: dir,
		DBPath: dir + "/sm.db", RootPassword: rootPW,
	}
	db, err := database.Connect(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Seed(db, cfg.BaseDomain); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	key, err := edge.LoadKey(dir + "/cookie.key")
	if err != nil {
		t.Fatal(err)
	}
	hub := tunnel.NewHub()
	a := api.New(db, cfg, reg, hub)
	e := edge.New(cfg, reg, hub, key, a.Handler())
	e.TokenUsed = a.TokenUsed
	a.AfterChange = e.Changed
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	en := &env{t: t, addr: strings.TrimPrefix(srv.URL, "http://"), srv: a, edge: e, dir: dir, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}}
	var login struct{ Token string }
	en.api("POST", "/v1/login", map[string]any{"password": rootPW}, 200, &login)
	en.token = login.Token
	return en
}

// req sends a request to the server with the given Host header.
func (e *env) req(method, host, path string, body io.Reader, hdr http.Header) *http.Response {
	e.t.Helper()
	r, err := http.NewRequest(method, "http://"+e.addr+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	r.Host = host
	for k, v := range hdr {
		r.Header[k] = v
	}
	res, err := e.client.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) api(method, path string, body any, want int, out any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if e.token != "" {
		hdr.Set("Authorization", "Bearer "+e.token)
	}
	res := e.req(method, "127.0.0.1", path, rd, hdr)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		e.t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// target is an upstream service that echoes what it received.
func target(t *testing.T, tls bool) (host string, port int) {
	t.Helper()
	up := websocket.Upgrader{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			n, _ := strconv.Atoi(r.URL.Query().Get("n"))
			w.Header().Set("Content-Length", strconv.Itoa(n))
			buf := bytes.Repeat([]byte("x"), 64<<10)
			for n > 0 {
				k := min(n, len(buf))
				_, _ = w.Write(buf[:k])
				n -= k
			}
			return
		}
		if r.URL.Path == "/ws" {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				mt, m, err := c.ReadMessage()
				if err != nil {
					return
				}
				_ = c.WriteMessage(mt, append([]byte("echo:"), m...))
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"path": r.URL.RequestURI(), "host": r.Host,
			"auth": r.Header.Get("Authorization"), "token": r.Header.Get("X-Supermatrix-Token"),
			"cookie": r.Header.Get("Cookie"), "xff": r.Header.Get("X-Forwarded-For"),
			"xfh": r.Header.Get("X-Forwarded-Host"), "user": r.Header.Get("X-Supermatrix-User"),
		})
	})
	var s *httptest.Server
	if tls {
		s = httptest.NewTLSServer(h)
	} else {
		s = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	p, _ := strconv.Atoi(u.Port())
	return u.Hostname(), p
}

func (e *env) startRelay(name string) (id string, stop func()) {
	e.t.Helper()
	id, stop, _ = e.startRelayWith(name, nil)
	return id, stop
}

// startRelayWith starts a relay, letting opt adjust it first.
func (e *env) startRelayWith(name string, opt func(*tunnel.Relay)) (id string, stop func(), relay *tunnel.Relay) {
	e.t.Helper()
	var out struct {
		Relay struct{ ID string }
		Token string
	}
	e.api("POST", "/v1/relays", map[string]any{"name": name}, 201, &out)
	ctx, cancel := context.WithCancel(context.Background())
	r := &tunnel.Relay{ServerURL: "http://" + e.addr, Token: out.Token, Version: "test", HealthEvery: time.Hour, Logf: func(string, ...any) {}}
	if opt != nil {
		opt(r)
	}
	go r.Run(ctx)
	e.waitFor(func() bool {
		var st struct{ Relays map[string]any }
		e.api("GET", "/v1/status", nil, 200, &st)
		_, ok := st.Relays[out.Relay.ID]
		return ok
	})
	e.e2eRelayToken = out.Token
	return out.Relay.ID, cancel, r
}

func (e *env) waitFor(cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatal("timed out waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type dep struct {
	ID     string
	Host   string
	Tokens []struct{ ID string }
}

// get fetches path on host until the relay has picked up the deployment.
func (e *env) get(host, path string, hdr http.Header) (*http.Response, string) {
	e.t.Helper()
	var res *http.Response
	var body string
	e.waitFor(func() bool {
		res = e.req("GET", host, path, nil, hdr)
		body = readBody(e.t, res)
		return !strings.Contains(body, "unknown deployment")
	})
	return res, body
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	relayID, stopRelay := e.startRelay("laptop")
	th, tp := target(t, false)

	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &d)
	if d.Host != "app.test" {
		t.Fatalf("host %q", d.Host)
	}

	t.Run("public", func(t *testing.T) {
		res, body := e.get("app.test", "/hello?x=1", nil)
		if res.StatusCode != 200 || !strings.Contains(body, `"path":"/hello?x=1"`) {
			t.Fatalf("%d %s", res.StatusCode, body)
		}
		// Host is rewritten to the target unless preserveHost is on, and the
		// public host is passed along.
		if !strings.Contains(body, fmt.Sprintf(`"host":"%s:%d"`, th, tp)) || !strings.Contains(body, `"xfh":"app.test"`) || !strings.Contains(body, `"xff":"127.0.0.1"`) {
			t.Fatalf("headers: %s", body)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"preserveHost": true}, 200, nil)
		_, body = e.get("app.test", "/", nil)
		if !strings.Contains(body, `"host":"app.test"`) {
			t.Fatalf("preserve host: %s", body)
		}
	})

	t.Run("websocket", func(t *testing.T) {
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
		c, _, err := dialer.Dial("ws://"+e.addr+"/ws", http.Header{"Host": {"app.test"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte("hi"))
		_, m, err := c.ReadMessage()
		if err != nil || string(m) != "echo:hi" {
			t.Fatalf("%q %v", m, err)
		}
	})

	t.Run("basic", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "basic", "basicUser": "ann", "basicPassword": "short"}, 400, nil)
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "basic", "basicUser": "ann", "basicPassword": "open sesame"}, 200, nil)
		res, _ := e.get("app.test", "/", nil)
		if res.StatusCode != 401 || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("no creds: %d", res.StatusCode)
		}
		bad := http.Header{"Authorization": {"Basic " + b64("ann:nope")}}
		if res, _ := e.get("app.test", "/", bad); res.StatusCode != 401 {
			t.Fatalf("wrong creds: %d", res.StatusCode)
		}
		good := http.Header{"Authorization": {"Basic " + b64("ann:open sesame")}}
		res, body := e.get("app.test", "/", good)
		if res.StatusCode != 200 || !strings.Contains(body, `"auth":""`) {
			t.Fatalf("good creds: %d %s", res.StatusCode, body)
		}
	})

	t.Run("token", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "token"}, 200, nil)
		var tok struct{ ID, Token string }
		e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "ci"}, 201, &tok)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 401 {
			t.Fatalf("no token: %d", res.StatusCode)
		}
		if res, _ := e.get("app.test", "/", http.Header{"Authorization": {"Bearer smt_wrong"}}); res.StatusCode != 401 {
			t.Fatalf("wrong token: %d", res.StatusCode)
		}
		res, body := e.get("app.test", "/", http.Header{"Authorization": {"Bearer " + tok.Token}})
		if res.StatusCode != 200 || !strings.Contains(body, `"auth":""`) {
			t.Fatalf("bearer: %d %s", res.StatusCode, body)
		}
		res, body = e.get("app.test", "/", http.Header{"X-Supermatrix-Token": {tok.Token}})
		if res.StatusCode != 200 || !strings.Contains(body, `"token":""`) {
			t.Fatalf("header: %d %s", res.StatusCode, body)
		}
		e.api("DELETE", "/v1/deployments/"+d.ID+"/tokens/"+tok.ID, nil, 200, nil)
		if res, _ := e.get("app.test", "/", http.Header{"Authorization": {"Bearer " + tok.Token}}); res.StatusCode != 401 {
			t.Fatalf("revoked token: %d", res.StatusCode)
		}
	})

	t.Run("login", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "login", "loginPassword": "deployment pw", "allowRootLogin": true}, 200, nil)
		html := http.Header{"Accept": {"text/html"}}
		res, _ := e.get("app.test", "/deep/page?q=1", html)
		if res.StatusCode != 303 || res.Header.Get("Location") != "/_supermatrix/login?next=%2Fdeep%2Fpage%3Fq%3D1" {
			t.Fatalf("redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
		}
		if res, _ := e.get("app.test", "/api", nil); res.StatusCode != 401 {
			t.Fatalf("api without session: %d", res.StatusCode)
		}
		res, body := e.get("app.test", "/_supermatrix/login?next=/deep", html)
		if res.StatusCode != 200 || !strings.Contains(body, "app.test") {
			t.Fatalf("login page: %d", res.StatusCode)
		}

		post := func(pw, next string) *http.Response {
			form := url.Values{"password": {pw}, "next": {next}}
			return e.req("POST", "app.test", "/_supermatrix/login", strings.NewReader(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
		}
		if res := post("nope", "/"); res.StatusCode != 401 {
			t.Fatalf("wrong password: %d", res.StatusCode)
		}
		res = post("deployment pw", "//evil.example/")
		if res.StatusCode != 303 || res.Header.Get("Location") != "/" {
			t.Fatalf("open redirect guard: %d %q", res.StatusCode, res.Header.Get("Location"))
		}
		cookie := res.Cookies()[0]
		withCookie := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value + "; theirs=1"}}
		res, body = e.get("app.test", "/", withCookie)
		if res.StatusCode != 200 || !strings.Contains(body, `"cookie":"theirs=1"`) {
			t.Fatalf("with session: %d %s", res.StatusCode, body)
		}
		tampered := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value[:len(cookie.Value)-2] + "xx"}}
		if res, _ := e.get("app.test", "/", tampered); res.StatusCode != 401 {
			t.Fatalf("tampered cookie: %d", res.StatusCode)
		}

		// Root password works while allowed, and those sessions end when
		// it's turned off.
		res = post(rootPW, "/")
		rootCookie := res.Cookies()[0]
		rootHdr := http.Header{"Cookie": {rootCookie.Name + "=" + rootCookie.Value}}
		if res, _ := e.get("app.test", "/", rootHdr); res.StatusCode != 200 {
			t.Fatalf("root session: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"allowRootLogin": false}, 200, nil)
		if res, _ := e.get("app.test", "/", rootHdr); res.StatusCode != 401 {
			t.Fatalf("root session after disallow: %d", res.StatusCode)
		}
		// Changing the deployment password signs its sessions out.
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"loginPassword": "a new password"}, 200, nil)
		if res, _ := e.get("app.test", "/", withCookie); res.StatusCode != 401 {
			t.Fatalf("session after password change: %d", res.StatusCode)
		}
	})

	t.Run("allowlist", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "none", "ipAllowlist": []string{"10.0.0.0/8"}}, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 403 {
			t.Fatalf("blocked: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{"10.0.0.0/8", "127.0.0.1"}}, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 200 {
			t.Fatalf("allowed: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{"not-an-ip"}}, 400, nil)
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{}}, 200, nil)
	})

	t.Run("https target", func(t *testing.T) {
		sh, sp := target(t, true)
		var sd dep
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "subdomain": "secure", "protocol": "https", "targetHost": sh, "targetPort": sp, "authMode": "none",
		}, 201, &sd)
		res, body := e.get("secure.test", "/", nil)
		if res.StatusCode != 502 || !strings.Contains(body, "couldn") {
			t.Fatalf("unverified self-signed should fail: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+sd.ID, map[string]any{"tlsSkipVerify": true}, 200, nil)
		var ok bool
		e.waitFor(func() bool {
			res, body = e.get("secure.test", "/x", nil)
			ok = res.StatusCode == 200 && strings.Contains(body, `"path":"/x"`)
			return ok
		})
	})

	t.Run("validation", func(t *testing.T) {
		e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "Bad_Sub", "targetHost": th, "targetPort": tp}, 400, nil)
		e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp}, 400, nil)
		e.api("POST", "/v1/deployments", map[string]any{"relayId": "nope", "subdomain": "x", "targetHost": th, "targetPort": tp}, 400, nil)
		e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "x", "targetHost": th, "targetPort": 0}, 400, nil)
		e.api("DELETE", "/v1/relays/"+relayID, nil, 409, nil)
	})

	t.Run("routing", func(t *testing.T) {
		if res := e.req("GET", "nope.test", "/", nil, nil); res.StatusCode != 404 {
			t.Fatalf("unknown sub: %d", res.StatusCode)
		}
		if res := e.req("GET", "elsewhere.example", "/", nil, nil); res.StatusCode != 404 {
			t.Fatalf("unknown host: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"enabled": false}, 200, nil)
		if res := e.req("GET", "app.test", "/", nil, nil); res.StatusCode != 404 {
			t.Fatalf("disabled: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"enabled": true}, 200, nil)
	})

	t.Run("relay offline", func(t *testing.T) {
		stopRelay()
		e.waitFor(func() bool {
			res := e.req("GET", "app.test", "/", nil, nil)
			return strings.Contains(readBody(t, res), "Relay offline") && res.StatusCode == 502
		})
	})
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t)
	tok := e.token
	e.token = ""
	e.api("GET", "/v1/relays", nil, 401, nil)
	e.api("POST", "/v1/login", map[string]any{"password": "wrong"}, 401, nil)
	e.token = "sms_bogus"
	e.api("GET", "/v1/relays", nil, 401, nil)
	e.token = tok
	e.api("GET", "/v1/relays", nil, 200, nil)
	e.api("POST", "/v1/logout", nil, 200, nil)
	e.api("GET", "/v1/relays", nil, 401, nil)

	// Rate limit: 10 tries a minute per address.
	e.token = ""
	for i := 0; i < 8; i++ {
		e.api("POST", "/v1/login", map[string]any{"password": "wrong"}, 401, nil)
	}
	e.api("POST", "/v1/login", map[string]any{"password": rootPW}, 429, nil)
}

func TestRelayTokenRotation(t *testing.T) {
	e := newEnv(t)
	id, stop := e.startRelay("pi")
	defer stop()
	old := e.e2eRelayToken
	var rot struct{ Token string }
	e.api("POST", "/v1/relays/"+id+"/rotate", nil, 200, &rot)
	if rot.Token == old || !strings.HasPrefix(rot.Token, "smr_") {
		t.Fatalf("rotated token %q", rot.Token)
	}
	// The old token no longer connects.
	dialer := websocket.Dialer{}
	_, res, err := dialer.Dial("ws://"+e.addr+"/v1/relay/connect", http.Header{"Authorization": {"Bearer " + old}})
	if err == nil || res == nil || res.StatusCode != 401 {
		t.Fatalf("old token accepted: %v", err)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
