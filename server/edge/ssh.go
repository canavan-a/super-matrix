package edge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"main/database"
	"main/registry"
	"main/tunnel"
)

// SSH deployments (protocol "ssh") aren't proxied as HTTP. Their host serves
// the usual sign-in pages, plus:
//
//	/_supermatrix/cli           the consent page that gives the supermatrix CLI a code
//	/_supermatrix/cli/exchange  where the CLI trades that code for a session
//	/_supermatrix/ssh           a WebSocket that is a raw pipe to the target (the CLI's ProxyCommand)
//	/                           the browser shell, or a page saying how to connect
//	/_supermatrix/shell         the browser shell's WebSocket
//
// The CLI's session is the same signed value as the browser cookie, sent
// back in a Cookie header, so it ends whenever a browser session would. It
// never passes through the browser: the consent page hands over a
// single-use code, good for a couple of minutes and only with the verifier
// whose hash the CLI sent first (as in OAuth's PKCE), so a code left in the
// browser's history, or read off the screen, is worth nothing.
//
// Open connections are checked again every SSHRecheck against the
// deployment as it is then: a changed password, a removed address or
// token, a turned-off toggle or a new allow list ends them too.

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     sameOrigin,
}

// sameOrigin lets the CLI (no Origin) and pages on the deployment's own host
// open the WebSockets and post the consent form, and no other site.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

var (
	cliStateRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	cliChallengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`) // base64url SHA-256
)

// cliCodeTTL is how long a consent code can be traded for a session.
const cliCodeTTL = 2 * time.Minute

type cliCode struct {
	deployment string
	session    string
	challenge  string
	expires    time.Time
}

// cliCodes are the consent codes not yet traded, by SHA-256.
type cliCodes struct {
	mu    sync.Mutex
	codes map[[32]byte]cliCode
}

func (c *cliCodes) add(code string, v cliCode, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.codes == nil {
		c.codes = map[[32]byte]cliCode{}
	}
	for k, old := range c.codes {
		if now.After(old.expires) {
			delete(c.codes, k)
		}
	}
	c.codes[sha256.Sum256([]byte(code))] = v
}

// take removes code and returns what it was for; a code works once, whether
// or not the verifier that comes with it is right.
func (c *cliCodes) take(code string, now time.Time) (cliCode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := sha256.Sum256([]byte(code))
	v, ok := c.codes[k]
	delete(c.codes, k)
	if !ok || now.After(v.expires) {
		return cliCode{}, false
	}
	return v, true
}

func randomCode() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge is base64url(SHA-256(verifier)).
func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (e *Edge) serveSSH(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) {
	if strings.HasPrefix(r.URL.Path, "/_supermatrix/assets/") {
		serveAsset(w, r)
		return
	}
	signIn := ent.AuthMode == database.AuthLogin || ent.AuthMode == database.AuthOTP
	switch r.URL.Path {
	case "/_supermatrix/login", "/_supermatrix/verify", "/_supermatrix/logout", "/_supermatrix/session":
		if signIn {
			e.loginRoutes(w, r, ent, ip)
			return
		}
	case "/_supermatrix/cli/exchange":
		// The CLI has no session yet; the code and verifier are the proof.
		e.cliExchange(w, r, ent, ip)
		return
	}
	// authorize strips the cookie and token, so keep a copy to check open
	// connections against later, and read the session first.
	orig := r.Clone(context.Background())
	var s session
	if signIn {
		s, _ = e.session(r, ent)
	}
	if !e.authorize(w, r, ent, ip) {
		return
	}
	switch r.URL.Path {
	case "/":
		if ent.SSHShell {
			e.shellPage(w, ent, signIn)
			return
		}
		render(w, http.StatusOK, "ssh-info", pageData{Title: "SSH", Host: ent.Host, CLI: ent.SSHCLI, SignOut: signIn})
	case "/_supermatrix/cli":
		e.cliConsent(w, r, ent, s, signIn)
	case "/_supermatrix/ssh":
		if !ent.SSHCLI {
			message(w, http.StatusNotFound, "Terminal access is off", "Turn on CLI access for this deployment in the app.")
			return
		}
		if !e.sshLimit.Allow(ip, e.now()) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many connections; wait a minute"})
			return
		}
		e.sshPipe(w, r, ent, orig, ip)
	case "/_supermatrix/shell":
		if !ent.SSHShell {
			message(w, http.StatusNotFound, "The shell is off", "Turn on the browser shell for this deployment in the app.")
			return
		}
		if !e.sshLimit.Allow(ip, e.now()) {
			http.Error(w, "too many connections; wait a minute", http.StatusTooManyRequests)
			return
		}
		e.shellSocket(w, r, ent, s, orig, ip)
	default:
		message(w, http.StatusNotFound, "Not found", "Nothing here.")
	}
}

// cliConsent asks a signed-in browser to let the CLI in. port is the CLI's
// loopback listener (0: show the code to paste instead); state is the CLI's
// nonce, passed back so it knows the answer is the one it asked for;
// challenge is the hash of the verifier the CLI must show to use the code.
func (e *Edge) cliConsent(w http.ResponseWriter, r *http.Request, ent *registry.Entry, s session, signIn bool) {
	if !ent.SSHCLI {
		message(w, http.StatusNotFound, "Terminal access is off", "Turn on CLI access for this deployment in the app.")
		return
	}
	if !signIn {
		message(w, http.StatusOK, "No sign-in needed", "This deployment doesn't use a browser sign-in, so the CLI connects without one.")
		return
	}
	if s.Kind == "" {
		// In with an API token, not a sign-in: there's no session to hand on.
		message(w, http.StatusForbidden, "Sign in first", "Terminal access is given by signing in to this deployment in the browser.")
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	state, challenge := r.FormValue("state"), r.FormValue("challenge")
	if err != nil || (port != 0 && (port < 1024 || port > 65535)) || !cliStateRe.MatchString(state) || !cliChallengeRe.MatchString(challenge) {
		message(w, http.StatusBadRequest, "Bad request", "Start this from the supermatrix CLI: supermatrix login "+ent.Host)
		return
	}
	d := pageData{Title: "Allow terminal access", Host: ent.Host, Port: port, State: state, Challenge: challenge, Who: who(s)}
	if r.Method != http.MethodPost {
		renderCSP(w, http.StatusOK, "cli", d, "form-action 'self' http://127.0.0.1:*")
		return
	}
	if !sameOrigin(r) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		message(w, http.StatusForbidden, "Not allowed", "That form came from another site.")
		return
	}
	code := randomCode()
	e.cliCodes.add(code, cliCode{
		deployment: ent.ID, session: e.mintSession(ent, s.Kind, s.Email, s.Expires),
		challenge: challenge, expires: e.now().Add(cliCodeTTL),
	}, e.now())
	if port == 0 {
		d.Title, d.Code = "Paste this into the terminal", code
		render(w, http.StatusOK, "cli-code", d)
		return
	}
	cb := fmt.Sprintf("http://127.0.0.1:%d/callback?state=%s&code=%s", port, url.QueryEscape(state), url.QueryEscape(code))
	http.Redirect(w, r, cb, http.StatusSeeOther)
}

// cliExchange trades a consent code and its verifier for the session.
func (e *Edge) cliExchange(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST the code and verifier"})
		return
	}
	if e.failLimit.Blocked(ip, e.now()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts; wait a minute"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	code, verifier := r.PostFormValue("code"), r.PostFormValue("verifier")
	v, ok := e.cliCodes.take(code, e.now())
	if !ok || v.deployment != ent.ID || len(verifier) < 43 ||
		subtle.ConstantTimeCompare([]byte(pkceChallenge(verifier)), []byte(v.challenge)) != 1 {
		e.failLimit.Allow(ip, e.now())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that code is wrong, used or expired; sign in again"})
		return
	}
	// Still a good session now (the password may have changed meanwhile)?
	if _, state := e.sessionValue(v.session, ent); state != authOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that sign-in has ended; sign in again"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session": v.session})
}

func who(s session) string {
	switch s.Kind {
	case "o":
		return s.Email
	case "r":
		return "the root password"
	}
	return "the deployment password"
}

// sshPipe connects the CLI to the target: WebSocket binary messages one
// way, the relay's stream the other.
func (e *Edge) sshPipe(w http.ResponseWriter, r *http.Request, ent *registry.Entry, orig *http.Request, ip string) {
	up, err := e.hub.Dial(r.Context(), ent.RelayID, ent.ID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": dialProblem(err)})
		return
	}
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		up.Close()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.watchAccess(ctx, orig, ent.ID, ip, false, func() {
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "access ended"), time.Now().Add(time.Second))
		ws.Close()
		up.Close()
	})
	tunnel.Pipe(tunnel.NewWSConn(ws), up)
}

// watchAccess calls end, once, when orig would no longer get in.
func (e *Edge) watchAccess(ctx context.Context, orig *http.Request, id, ip string, shell bool, end func()) {
	t := time.NewTicker(e.SSHRecheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !e.stillAllowed(orig, id, ip, shell) {
				end()
				return
			}
		}
	}
}

// stillAllowed runs the checks a new connection would get, against the
// deployment as it is now.
func (e *Edge) stillAllowed(orig *http.Request, id, ip string, shell bool) bool {
	ent := e.reg.ByID(id)
	if ent == nil || !ent.Enabled || ent.Protocol != database.ProtocolSSH || !ent.Allowed(ip) {
		return false
	}
	if (shell && !ent.SSHShell) || (!shell && !ent.SSHCLI) {
		return false
	}
	return e.authorize(discardResponse{}, orig.Clone(context.Background()), ent, ip)
}

// discardResponse lets authorize run as a check, its answer thrown away.
type discardResponse struct{}

func (discardResponse) Header() http.Header         { return http.Header{} }
func (discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (discardResponse) WriteHeader(int)             {}

// dialProblem says why the relay couldn't connect, in words.
func dialProblem(err error) string {
	var te *tunnel.TargetError
	switch {
	case errors.Is(err, tunnel.ErrOffline):
		return "the relay serving this deployment isn't connected right now"
	case errors.As(err, &te):
		return "the relay is up but couldn't connect to the service behind it: " + te.Msg
	}
	return "couldn't reach the service: " + err.Error()
}
