package e2etest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"main/database"
)

// fakeMail keeps the last code sent to each address.
type fakeMail struct {
	mu    sync.Mutex
	codes map[string]string
}

func (f *fakeMail) Ready() bool { return true }

func (f *fakeMail) SendCode(to, _, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[to] = code
	return nil
}

func (f *fakeMail) code(to string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes[to]
}

func form(url.Values) http.Header {
	return http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
}

func TestEmailCode(t *testing.T) {
	e := newEnv(t)
	mail := &fakeMail{codes: map[string]string{}}
	e.edge.Mail = mail
	relayID, stop := e.startRelay("laptop")
	defer stop()
	th, tp := target(t, false)

	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "otp", "targetHost": th, "targetPort": tp, "authMode": "otp",
	}, 400, nil) // needs an address
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "otp", "targetHost": th, "targetPort": tp, "authMode": "otp",
		"otpEmails": []string{"not an email"},
	}, 400, nil)
	var d struct {
		ID        string
		OTPEmails []string `json:"otpEmails"`
	}
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "otp", "targetHost": th, "targetPort": tp, "authMode": "otp",
		"otpEmails": []string{" Ann@Example.com", "ann@example.com", "", "bob@example.com"}, "sessionTtlS": 900,
	}, 201, &d)
	if strings.Join(d.OTPEmails, ",") != "ann@example.com,bob@example.com" {
		t.Fatalf("emails not normalized: %v", d.OTPEmails)
	}

	html := http.Header{"Accept": {"text/html"}}
	res, _ := e.get("otp.test", "/app?x=1", html)
	if res.StatusCode != 303 || res.Header.Get("Location") != "/_supermatrix/login?next=%2Fapp%3Fx%3D1" {
		t.Fatalf("redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	// Backend calls get a 401 they can act on.
	res, body := e.get("otp.test", "/api/things", nil)
	if res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "required" || !strings.Contains(body, `"login":"/_supermatrix/login?next=%2Fapi%2Fthings"`) {
		t.Fatalf("api without session: %d %q %s", res.StatusCode, res.Header.Get("X-Supermatrix-Auth"), body)
	}
	if res, body := e.get("otp.test", "/_supermatrix/login", html); res.StatusCode != 200 || !strings.Contains(body, `name="email"`) {
		t.Fatalf("email page: %d", res.StatusCode)
	}

	post := func(path string, v url.Values) (*http.Response, string) {
		res := e.req("POST", "otp.test", path, strings.NewReader(v.Encode()), form(v))
		return res, readBody(t, res)
	}

	// Someone not on the list sees the same page, and gets nothing.
	res, body = post("/_supermatrix/login", url.Values{"email": {"eve@example.com"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Check your email") {
		t.Fatalf("unlisted: %d", res.StatusCode)
	}
	res, body = post("/_supermatrix/login", url.Values{"email": {"ANN@example.com"}, "next": {"/app"}})
	if res.StatusCode != 200 || !strings.Contains(body, "Check your email") {
		t.Fatalf("listed: %d", res.StatusCode)
	}
	e.waitFor(func() bool { return mail.code("ann@example.com") != "" })
	if mail.code("eve@example.com") != "" {
		t.Fatal("sent a code to an address that isn't on the list")
	}
	code := mail.code("ann@example.com")

	if res, _ := post("/_supermatrix/verify", url.Values{"email": {"ann@example.com"}, "code": {"000000x"}}); res.StatusCode != 401 {
		t.Fatalf("wrong code: %d", res.StatusCode)
	}
	if res, _ := post("/_supermatrix/verify", url.Values{"email": {"eve@example.com"}, "code": {code}}); res.StatusCode != 401 {
		t.Fatalf("code for someone else: %d", res.StatusCode)
	}
	res, _ = post("/_supermatrix/verify", url.Values{"email": {"ann@example.com"}, "code": {code}, "next": {"/app"}})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/app" {
		t.Fatalf("verify: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	cookie := res.Cookies()[0]
	// The cookie outlives the session so an expired one can be recognized.
	if cookie.MaxAge <= 900 {
		t.Fatalf("cookie max-age %d", cookie.MaxAge)
	}
	if res, _ := post("/_supermatrix/verify", url.Values{"email": {"ann@example.com"}, "code": {code}}); res.StatusCode != 401 {
		t.Fatalf("code reused: %d", res.StatusCode)
	}

	withCookie := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value + "; theirs=1"}, "X-Supermatrix-User": {"forged@example.com"}}
	res, body = e.get("otp.test", "/api/things", withCookie)
	if res.StatusCode != 200 || !strings.Contains(body, `"user":"ann@example.com"`) || !strings.Contains(body, `"cookie":"theirs=1"`) {
		t.Fatalf("with session: %d %s", res.StatusCode, body)
	}

	var s struct {
		SignedIn  bool
		Email     string
		ExpiresAt time.Time
	}
	res, body = e.get("otp.test", "/_supermatrix/session", withCookie)
	if err := json.Unmarshal([]byte(body), &s); err != nil || res.StatusCode != 200 || !s.SignedIn || s.Email != "ann@example.com" ||
		s.ExpiresAt.Sub(time.Now()) > 15*time.Minute || s.ExpiresAt.Sub(time.Now()) < 14*time.Minute {
		t.Fatalf("session: %d %s", res.StatusCode, body)
	}

	// A session past its time says so, to pages and to backend calls.
	key, err := os.ReadFile(e.dir + "/cookie.key")
	if err != nil {
		t.Fatal(err)
	}
	payload := d.ID + "|" + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10) + "|o|ann@example.com"
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload + "|otp\x00ann@example.com"))
	expired := http.Header{"Cookie": {cookie.Name + "=" + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))}}
	res, _ = e.get("otp.test", "/api/things", expired)
	if res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "expired" {
		t.Fatalf("expired api: %d %q", res.StatusCode, res.Header.Get("X-Supermatrix-Auth"))
	}
	res, _ = e.get("otp.test", "/app", http.Header{"Cookie": expired["Cookie"], "Accept": {"text/html"}})
	if res.StatusCode != 303 || !strings.HasSuffix(res.Header.Get("Location"), "&expired=1") {
		t.Fatalf("expired page: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res, body := e.get("otp.test", "/_supermatrix/login?expired=1", html); !strings.Contains(body, "session expired") {
		t.Fatalf("expired notice: %d", res.StatusCode)
	}

	// *@domain lets in everyone at that domain, and nothing else.
	for _, bad := range []string{"a*@example.com", "*@*.com", "*@ex*ample.com"} {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"otpEmails": []string{bad}}, 400, nil)
	}
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"otpEmails": []string{"*@Corp.com", "ann@example.com"}}, 200, nil)
	post("/_supermatrix/login", url.Values{"email": {"cy@corp.com"}})
	post("/_supermatrix/login", url.Values{"email": {"dee@sub.corp.com"}})
	post("/_supermatrix/login", url.Values{"email": {"*@corp.com"}})
	e.waitFor(func() bool { return mail.code("cy@corp.com") != "" })
	if mail.code("dee@sub.corp.com") != "" || mail.code("*@corp.com") != "" {
		t.Fatal("wildcard matched more than its domain")
	}
	res, _ = post("/_supermatrix/verify", url.Values{"email": {"cy@corp.com"}, "code": {mail.code("cy@corp.com")}})
	if res.StatusCode != 303 {
		t.Fatalf("wildcard verify: %d", res.StatusCode)
	}
	cyCookie := http.Header{"Cookie": {res.Cookies()[0].Name + "=" + res.Cookies()[0].Value}}
	if res, _ := e.get("otp.test", "/", cyCookie); res.StatusCode != 200 {
		t.Fatalf("wildcard session: %d", res.StatusCode)
	}

	// Taking someone off the list signs them out.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"otpEmails": []string{"bob@example.com"}}, 200, nil)
	if res, _ := e.get("otp.test", "/", withCookie); res.StatusCode != 401 {
		t.Fatalf("after removal: %d", res.StatusCode)
	}

	// A session from email codes doesn't carry over to password login.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "login", "otpEmails": []string{"ann@example.com"}}, 200, nil)
	if res, _ := e.get("otp.test", "/", withCookie); res.StatusCode != 401 {
		t.Fatalf("otp session in login mode: %d", res.StatusCode)
	}
}

func TestTokenExpiry(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("laptop")
	defer stop()
	th, tp := target(t, false)
	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "api", "targetHost": th, "targetPort": tp, "authMode": "token",
	}, 201, &d)

	e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "ci", "expiresInS": 10}, 400, nil)
	var forever, short struct {
		ID        string
		Token     string
		ExpiresAt *time.Time
	}
	e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "forever"}, 201, &forever)
	e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "ci", "expiresInS": 3600}, 201, &short)
	if forever.ExpiresAt != nil || short.ExpiresAt == nil || short.ExpiresAt.Sub(time.Now()) < 59*time.Minute {
		t.Fatalf("expiry: %v %v", forever.ExpiresAt, short.ExpiresAt)
	}
	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	if res, _ := e.get("api.test", "/", bearer(short.Token)); res.StatusCode != 200 {
		t.Fatalf("fresh token: %d", res.StatusCode)
	}

	// Push it into the past, then make the server reload.
	db, err := database.Connect(e.dir + "/sm.db")
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&database.DeploymentToken{}).Where("id = ?", short.ID).Update("expires_at", time.Now().Add(-time.Second))
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"enabled": true}, 200, nil)

	res, _ := e.get("api.test", "/", bearer(short.Token))
	if res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "token_expired" || !strings.Contains(res.Header.Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("expired token: %d %q", res.StatusCode, res.Header.Get("X-Supermatrix-Auth"))
	}
	if res, _ := e.get("api.test", "/", bearer(forever.Token)); res.StatusCode != 200 {
		t.Fatalf("never-expiring token: %d", res.StatusCode)
	}
	if res, _ := e.get("api.test", "/", bearer("smt_nope")); res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "token_invalid" {
		t.Fatalf("unknown token: %d", res.StatusCode)
	}
	if res, _ := e.get("api.test", "/", nil); res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "required" {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	// In a sign-in mode an expired token still fails as a token, page loads
	// included, rather than going to the sign-in page.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "login", "loginPassword": "deployment pw"}, 200, nil)
	for _, hdr := range []http.Header{
		{"Authorization": {"Bearer " + short.Token}, "Accept": {"text/html"}},
		{"X-Supermatrix-Token": {short.Token}, "Accept": {"text/html"}},
	} {
		res, body := e.get("api.test", "/page", hdr)
		if res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "token_expired" || !strings.Contains(body, `"auth":"token_expired"`) {
			t.Fatalf("expired token in login mode: %d %q %s", res.StatusCode, res.Header.Get("X-Supermatrix-Auth"), body)
		}
	}
	res, body := e.get("api.test", "/page", http.Header{"X-Supermatrix-Token": {"smt_nope"}, "Accept": {"text/html"}})
	if res.StatusCode != 401 || res.Header.Get("X-Supermatrix-Auth") != "token_invalid" || !strings.Contains(body, `"auth":"token_invalid"`) {
		t.Fatalf("unknown token header in login mode: %d %s", res.StatusCode, body)
	}
}

// TestTokenOverride: a deployment's API token gets in whatever the auth
// mode, instead of signing in; an unknown Bearer token is left for the
// service.
func TestTokenOverride(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("laptop")
	defer stop()
	th, tp := target(t, false)
	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp,
		"authMode": "login", "loginPassword": "deployment pw",
	}, 201, &d)
	var tok struct{ ID, Token string }
	e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "ci"}, 201, &tok)
	bearer := http.Header{"Authorization": {"Bearer " + tok.Token}}

	for _, mode := range []map[string]any{
		{"authMode": "login"},
		{"authMode": "otp", "otpEmails": []string{"ann@example.com"}},
		{"authMode": "basic", "basicUser": "ann", "basicPassword": "open sesame"},
		{"authMode": "token"},
	} {
		e.api("PATCH", "/v1/deployments/"+d.ID, mode, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 401 {
			t.Fatalf("%v without credentials: %d", mode["authMode"], res.StatusCode)
		}
		res, body := e.get("app.test", "/", bearer)
		if res.StatusCode != 200 || !strings.Contains(body, `"auth":""`) {
			t.Fatalf("%v bearer: %d %s", mode["authMode"], res.StatusCode, body)
		}
		res, body = e.get("app.test", "/", http.Header{"X-Supermatrix-Token": {tok.Token}})
		if res.StatusCode != 200 || !strings.Contains(body, `"token":""`) {
			t.Fatalf("%v header: %d %s", mode["authMode"], res.StatusCode, body)
		}
		if res, _ := e.get("app.test", "/", http.Header{"X-Supermatrix-Token": {"smt_nope"}}); res.StatusCode != 401 {
			t.Fatalf("%v unknown token header: %d", mode["authMode"], res.StatusCode)
		}
	}

	// Basic mode: an unknown Bearer token isn't ours, so basic auth decides.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "basic"}, 200, nil)
	res, _ := e.get("app.test", "/", http.Header{"Authorization": {"Bearer theirs"}})
	if res.StatusCode != 401 || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("basic with a foreign bearer: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}

	// Login mode: a signed-in browser's own Bearer token reaches the service.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "login"}, 200, nil)
	v := url.Values{"password": {"deployment pw"}}
	login := e.req("POST", "app.test", "/_supermatrix/login", strings.NewReader(v.Encode()), form(v))
	c := login.Cookies()[0]
	res, body := e.get("app.test", "/", http.Header{"Cookie": {c.Name + "=" + c.Value}, "Authorization": {"Bearer theirs"}})
	if res.StatusCode != 200 || !strings.Contains(body, `"auth":"Bearer theirs"`) {
		t.Fatalf("foreign bearer with session: %d %s", res.StatusCode, body)
	}

	// Public: tokens play no part, headers go through untouched.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "none"}, 200, nil)
	if res, body := e.get("app.test", "/", http.Header{"X-Supermatrix-Token": {"smt_nope"}}); res.StatusCode != 200 || !strings.Contains(body, `"token":"smt_nope"`) {
		t.Fatalf("public: %d %s", res.StatusCode, body)
	}

	// Revoked: back to signing in.
	e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"authMode": "login"}, 200, nil)
	e.api("DELETE", "/v1/deployments/"+d.ID+"/tokens/"+tok.ID, nil, 200, nil)
	if res, _ := e.get("app.test", "/", bearer); res.StatusCode != 401 {
		t.Fatalf("revoked token: %d", res.StatusCode)
	}
}
