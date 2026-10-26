package e2etest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"main/tunnel"
)

const (
	sshUser = "ann"
	sshPass = "sshd password"
)

// sshTarget is an sshd that takes ann's password, answers exec with the
// command, and runs a "shell" that greets and echoes. Its host key can be
// swapped to test the shell's host key check.
func sshTarget(t *testing.T) (host string, port int, newKey func()) {
	t.Helper()
	var key atomic.Pointer[ssh.Signer]
	newKey = func() {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		s, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		key.Store(&s)
	}
	newKey()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cfg := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
				if m.User() == sshUser && string(pw) == sshPass {
					return nil, nil
				}
				return nil, fmt.Errorf("no")
			}}
			cfg.AddHostKey(*key.Load())
			go serveSSH(c, cfg)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port, newKey
}

func serveSSH(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				switch r.Type {
				case "pty-req", "window-change":
					_ = r.Reply(true, nil)
				case "exec":
					_ = r.Reply(true, nil)
					cmd := string(r.Payload[4:])
					_, _ = io.WriteString(ch, "ran: "+cmd+"\n")
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				case "shell":
					_ = r.Reply(true, nil)
					_, _ = io.WriteString(ch, "welcome\r\n$ ")
					go func() {
						buf := make([]byte, 1024)
						for {
							n, err := ch.Read(buf)
							if err != nil {
								return
							}
							_, _ = ch.Write(append([]byte("echo:"), buf[:n]...))
						}
					}()
				default:
					_ = r.Reply(false, nil)
				}
			}
		}()
	}
}

// sshDial opens the CLI's pipe to host with hdr; on failure it returns the
// HTTP status and body.
func (e *env) sshDial(host, path string, hdr http.Header) (*websocket.Conn, int, string) {
	e.t.Helper()
	h := http.Header{"Host": {host}}
	for k, v := range hdr {
		h[k] = v
	}
	ws, res, err := websocket.DefaultDialer.Dial("ws://"+e.addr+path, h)
	if err == nil {
		return ws, 101, ""
	}
	if res == nil {
		e.t.Fatal(err)
	}
	return nil, res.StatusCode, readBody(e.t, res)
}

// sshRun runs cmd over the CLI pipe and returns its output.
func (e *env) sshRun(host string, hdr http.Header, cmd string) string {
	e.t.Helper()
	var ws *websocket.Conn
	e.waitFor(func() bool {
		var code int
		var body string
		ws, code, body = e.sshDial(host, "/_supermatrix/ssh", hdr)
		if code != 101 && code != http.StatusBadGateway {
			e.t.Fatalf("ssh pipe: %d %s", code, body)
		}
		return ws != nil
	})
	conn := tunnel.NewWSConn(ws)
	defer conn.Close()
	c, chans, reqs, err := ssh.NewClientConn(conn, host, &ssh.ClientConfig{
		User: sshUser, Auth: []ssh.AuthMethod{ssh.Password(sshPass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	s, err := client.NewSession()
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := s.Output(cmd)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(out)
}

var codeRe = regexp.MustCompile(`aria-label="Code">([^<]+)</textarea>`)

// The CLI's side of the consent: its nonce and PKCE verifier.
const (
	cliState    = "state-0123456789abcdef"
	cliVerifier = "verifier-0123456789abcdefghijklmnopqrstuvwxyz"
)

func cliQuery(port int) string {
	h := sha256.Sum256([]byte(cliVerifier))
	return url.Values{"port": {strconv.Itoa(port)}, "state": {cliState}, "challenge": {base64.RawURLEncoding.EncodeToString(h[:])}}.Encode()
}

// exchange trades a consent code for a session, as the CLI does.
func (e *env) exchange(host, code, verifier string) (int, map[string]string) {
	e.t.Helper()
	res := e.req("POST", host, "/_supermatrix/cli/exchange", strings.NewReader(url.Values{"code": {code}, "verifier": {verifier}}.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	var out map[string]string
	_ = json.Unmarshal([]byte(readBody(e.t, res)), &out)
	return res.StatusCode, out
}

// consentCode allows the CLI on the consent page (form carries the cookie)
// and trades the code for the session.
func (e *env) consentCode(form http.Header, port int, verifier string) string {
	e.t.Helper()
	res := e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(port)), form)
	m := codeRe.FindStringSubmatch(readBody(e.t, res))
	if m == nil {
		e.t.Fatalf("no code: %d", res.StatusCode)
	}
	code, out := e.exchange("box.test", html.UnescapeString(strings.TrimSpace(m[1])), verifier)
	if code != 200 || out["session"] == "" {
		e.t.Fatalf("exchange: %d %v", code, out)
	}
	return out["session"]
}

func TestSSH(t *testing.T) {
	e := newEnv(t)
	e.edge.HostKeySeen = e.srv.SSHHostKeySeen
	e.edge.SSHRecheck = 50 * time.Millisecond
	relayID, stop := e.startRelay("box")
	defer stop()
	th, tp, newKey := sshTarget(t)

	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "box", "protocol": "ssh", "targetHost": th, "targetPort": tp,
		"authMode": "login", "loginPassword": "box password", "allowRootLogin": false,
	}, 201, &d)
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "other", "protocol": "ssh", "targetHost": th, "targetPort": tp,
		"authMode": "login", "loginPassword": "box password", "allowRootLogin": false,
	}, 201, nil)
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "bad", "protocol": "ssh", "targetHost": th, "targetPort": tp,
		"authMode": "basic", "basicUser": "x", "basicPassword": "12345678",
	}, 400, nil)

	signIn := func(pw string) http.Header {
		res := e.req("POST", "box.test", "/_supermatrix/login", strings.NewReader(url.Values{"password": {pw}}.Encode()),
			http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
		readBody(t, res)
		if res.StatusCode != 303 || len(res.Cookies()) == 0 {
			t.Fatalf("sign-in: %d", res.StatusCode)
		}
		c := res.Cookies()[0]
		return http.Header{"Cookie": {c.Name + "=" + c.Value}}
	}

	t.Run("cli needs a sign-in", func(t *testing.T) {
		_, code, body := e.sshDial("box.test", "/_supermatrix/ssh", nil)
		if code != 401 || !strings.Contains(body, `"login"`) {
			t.Fatalf("%d %s", code, body)
		}
		// The consent page sends a signed-out browser to sign in, and back.
		res := e.req("GET", "box.test", "/_supermatrix/cli?port=0&state="+cliState+"", nil, http.Header{"Accept": {"text/html"}})
		readBody(t, res)
		if loc := res.Header.Get("Location"); res.StatusCode != 303 || !strings.Contains(loc, "next=%2F_supermatrix%2Fcli%3Fport%3D0") {
			t.Fatalf("%d %s", res.StatusCode, loc)
		}
		res = e.req("POST", "box.test", "/_supermatrix/login", strings.NewReader(url.Values{"password": {"box password"}, "next": {"/_supermatrix/cli?port=0&state=" + cliState + ""}}.Encode()),
			http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
		readBody(t, res)
		if loc := res.Header.Get("Location"); loc != "/_supermatrix/cli?port=0&state="+cliState+"" {
			t.Fatalf("after sign-in: %q", loc)
		}
	})

	cookie := signIn("box password")
	var session string
	t.Run("consent hands the CLI a code, worth a session only with the verifier", func(t *testing.T) {
		res := e.req("GET", "box.test", "/_supermatrix/cli?"+cliQuery(0), nil, cookie)
		if body := readBody(t, res); res.StatusCode != 200 || !strings.Contains(body, "Allow terminal access") || !strings.Contains(body, "the deployment password") {
			t.Fatalf("%d %s", res.StatusCode, body)
		}
		// Without a challenge there's no consent.
		res = e.req("GET", "box.test", "/_supermatrix/cli?port=0&state="+cliState, nil, cookie)
		if readBody(t, res); res.StatusCode != 400 {
			t.Fatalf("no challenge: %d", res.StatusCode)
		}
		form := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": cookie["Cookie"]}
		res = e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(0)), form)
		body := readBody(t, res)
		m := codeRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no code on the page: %s", body)
		}
		code := html.UnescapeString(strings.TrimSpace(m[1]))
		if strings.Contains(code, ".") {
			t.Fatal("the page shows a session, not a code")
		}
		// A wrong verifier burns the code.
		if code, out := e.exchange("box.test", code, "x"+cliVerifier[1:]); code != 400 {
			t.Fatalf("wrong verifier: %d %v", code, out)
		}
		if code, _ := e.exchange("box.test", code, cliVerifier); code != 400 {
			t.Fatalf("burned code: %d", code)
		}
		session = e.consentCode(form, 0, cliVerifier)
		// It works once.
		res = e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(0)), form)
		code = html.UnescapeString(strings.TrimSpace(codeRe.FindStringSubmatch(readBody(t, res))[1]))
		if c, _ := e.exchange("box.test", code, cliVerifier); c != 200 {
			t.Fatalf("exchange: %d", c)
		}
		if c, _ := e.exchange("box.test", code, cliVerifier); c != 400 {
			t.Fatalf("replayed code: %d", c)
		}
		// Not on another deployment.
		res = e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(0)), form)
		code = html.UnescapeString(strings.TrimSpace(codeRe.FindStringSubmatch(readBody(t, res))[1]))
		if c, _ := e.exchange("other.test", code, cliVerifier); c != 400 {
			t.Fatalf("code on another deployment: %d", c)
		}
		// With a port, only the code goes to the CLI's loopback listener.
		res = e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(45678)), form)
		readBody(t, res)
		loc := res.Header.Get("Location")
		if res.StatusCode != 303 || !strings.HasPrefix(loc, "http://127.0.0.1:45678/callback?state="+cliState+"&code=") || strings.Contains(loc, "session") {
			t.Fatalf("%d %s", res.StatusCode, loc)
		}
		// Another site can't post it.
		form.Set("Origin", "http://evil.example")
		if res = e.req("POST", "box.test", "/_supermatrix/cli", strings.NewReader(cliQuery(45678)), form); res.StatusCode != 403 {
			t.Fatalf("cross-site consent: %d", res.StatusCode)
		}
		readBody(t, res)
	})

	cli := http.Header{"Cookie": {"supermatrix=" + session}}
	t.Run("ssh over the pipe", func(t *testing.T) {
		if out := e.sshRun("box.test", cli, "uptime"); out != "ran: uptime\n" {
			t.Fatalf("%q", out)
		}
	})

	t.Run("api token", func(t *testing.T) {
		var tok struct{ Token string }
		e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "ci"}, 201, &tok)
		if out := e.sshRun("box.test", http.Header{"X-Supermatrix-Token": {tok.Token}}, "id"); out != "ran: id\n" {
			t.Fatalf("%q", out)
		}
	})

	t.Run("shell", func(t *testing.T) {
		hdr := http.Header{"Cookie": cookie["Cookie"], "Origin": {"http://box.test"}}
		if _, code, _ := e.sshDial("box.test", "/_supermatrix/shell", http.Header{"Cookie": cookie["Cookie"], "Origin": {"http://evil.example"}}); code != 403 {
			t.Fatalf("cross-site shell: %d", code)
		}
		ws, code, body := e.sshDial("box.test", "/_supermatrix/shell", hdr)
		if ws == nil {
			t.Fatalf("%d %s", code, body)
		}
		defer ws.Close()
		sh := &shellConn{t: t, ws: ws}
		sh.expect("login as: ")
		sh.send(sshUser + "\r")
		sh.expect("saved the host key")
		sh.expect("password: ")
		sh.send(sshPass + "\r")
		sh.expect("welcome")
		sh.send("ls")
		sh.expect("echo:ls")

		var got struct{ SSHHostKey string }
		e.waitFor(func() bool {
			e.api("GET", "/v1/deployments/"+d.ID, nil, 200, &got)
			return strings.HasPrefix(got.SSHHostKey, "SHA256:")
		})
	})

	t.Run("host key change is refused", func(t *testing.T) {
		newKey()
		ws, _, _ := e.sshDial("box.test", "/_supermatrix/shell", http.Header{"Cookie": cookie["Cookie"]})
		defer ws.Close()
		sh := &shellConn{t: t, ws: ws}
		sh.expect("login as: ")
		sh.send(sshUser + "\r")
		sh.expect("host key changed")
		// Reset in the app: the next key is trusted.
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"sshHostKey": ""}, 200, nil)
		ws2, _, _ := e.sshDial("box.test", "/_supermatrix/shell", http.Header{"Cookie": cookie["Cookie"]})
		defer ws2.Close()
		sh = &shellConn{t: t, ws: ws2}
		sh.expect("login as: ")
		sh.send(sshUser + "\r")
		sh.expect("password: ")
		sh.send(sshPass + "\r")
		sh.expect("welcome")
	})

	t.Run("toggles", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"sshShell": false}, 200, nil)
		res := e.req("GET", "box.test", "/", nil, cookie)
		if body := readBody(t, res); !strings.Contains(body, "ProxyCommand supermatrix access %h") {
			t.Fatalf("info page: %s", body)
		}
		if _, code, _ := e.sshDial("box.test", "/_supermatrix/shell", cookie); code != 404 {
			t.Fatalf("shell off: %d", code)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"sshCli": false}, 200, nil)
		if _, code, _ := e.sshDial("box.test", "/_supermatrix/ssh", cli); code != 404 {
			t.Fatalf("cli off: %d", code)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"sshCli": true, "sshShell": true}, 200, nil)
	})

	t.Run("an API token can't mint a CLI session", func(t *testing.T) {
		var tok struct{ Token string }
		e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "mint"}, 201, &tok)
		res := e.req("GET", "box.test", "/_supermatrix/cli?"+cliQuery(0), nil, http.Header{"X-Supermatrix-Token": {tok.Token}})
		if readBody(t, res); res.StatusCode != 403 {
			t.Fatalf("%d", res.StatusCode)
		}
	})

	t.Run("revoking access ends open connections", func(t *testing.T) {
		// open is a raw pipe that has shown the sshd's banner; closed waits
		// for the server to end it.
		open := func(hdr http.Header) *websocket.Conn {
			var ws *websocket.Conn
			e.waitFor(func() bool { ws, _, _ = e.sshDial("box.test", "/_supermatrix/ssh", hdr); return ws != nil })
			_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, b, err := ws.ReadMessage(); err != nil || !strings.HasPrefix(string(b), "SSH-2.0-") {
				t.Fatalf("banner: %q %v", b, err)
			}
			return ws
		}
		closed := func(ws *websocket.Conn, what string) {
			_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				if _, _, err := ws.ReadMessage(); err != nil {
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						t.Fatalf("%s: still open", what)
					}
					return
				}
			}
		}

		var tok struct{ ID, Token string }
		e.api("POST", "/v1/deployments/"+d.ID+"/tokens", map[string]any{"name": "revoke-me"}, 201, &tok)
		ws := open(http.Header{"X-Supermatrix-Token": {tok.Token}})
		e.api("DELETE", "/v1/deployments/"+d.ID+"/tokens/"+tok.ID, nil, 200, nil)
		closed(ws, "revoked token")

		ws = open(cli)
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{"192.0.2.1"}}, 200, nil)
		closed(ws, "allow list")
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{}}, 200, nil)

		shell, _, _ := e.sshDial("box.test", "/_supermatrix/shell", cookie)
		defer shell.Close()
		(&shellConn{t: t, ws: shell}).expect("login as: ")
		ws = open(cli)
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"loginPassword": "a new password"}, 200, nil)
		closed(ws, "new password (pipe)")
		closed(shell, "new password (shell)")
	})

	t.Run("new password ends CLI sessions", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"loginPassword": "another password"}, 200, nil)
		if _, code, _ := e.sshDial("box.test", "/_supermatrix/ssh", cli); code != 401 {
			t.Fatalf("old session: %d", code)
		}
	})
}

// shellConn reads the browser shell's output until it shows what's expected.
type shellConn struct {
	t   *testing.T
	ws  *websocket.Conn
	buf bytes.Buffer
}

func (s *shellConn) expect(want string) {
	s.t.Helper()
	_ = s.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !strings.Contains(s.buf.String(), want) {
		mt, b, err := s.ws.ReadMessage()
		if err != nil {
			s.t.Fatalf("waiting for %q: %v; got %q", want, err, s.buf.String())
		}
		if mt == websocket.BinaryMessage {
			s.buf.Write(b)
		} else if strings.Contains(string(b), `"exit"`) && !strings.Contains(s.buf.String(), want) {
			s.buf.Write(b)
		}
	}
	i := strings.Index(s.buf.String(), want)
	rest := s.buf.String()[i+len(want):]
	s.buf.Reset()
	s.buf.WriteString(rest)
}

func (s *shellConn) send(text string) {
	s.t.Helper()
	if err := s.ws.WriteMessage(websocket.BinaryMessage, []byte(text)); err != nil {
		s.t.Fatal(err)
	}
}
