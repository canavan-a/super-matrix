package edge

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"

	"main/database"
	"main/registry"
	"main/tunnel"
)

// SSH deployments (protocol "ssh") aren't proxied as HTTP. Their host serves
// the usual sign-in pages, plus:
//
//	/_supermatrix/cli     the consent page that hands the supermatrix CLI a session
//	/_supermatrix/ssh     a WebSocket that is a raw pipe to the target (the CLI's ProxyCommand)
//	/                     the browser shell, or a page saying how to connect
//	/_supermatrix/shell   the browser shell's WebSocket
//
// The CLI's session is the same signed value as the browser cookie, sent
// back in a Cookie header, so it ends whenever a browser session would.

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     sameOrigin,
}

// sameOrigin lets the CLI (no Origin) and pages on the deployment's own host
// open the WebSockets, and no other site.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

var cliStateRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

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
	}
	// authorize strips the cookie, so read the session first.
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
		e.sshPipe(w, r, ent)
	case "/_supermatrix/shell":
		if !ent.SSHShell {
			message(w, http.StatusNotFound, "The shell is off", "Turn on the browser shell for this deployment in the app.")
			return
		}
		e.shellSocket(w, r, ent, s)
	default:
		message(w, http.StatusNotFound, "Not found", "Nothing here.")
	}
}

// cliConsent asks a signed-in browser to hand the CLI a session. port is the
// CLI's loopback listener (0: show the session to paste instead); state is
// the CLI's nonce, passed back so it knows the answer is the one it asked for.
func (e *Edge) cliConsent(w http.ResponseWriter, r *http.Request, ent *registry.Entry, s session, signIn bool) {
	if !ent.SSHCLI {
		message(w, http.StatusNotFound, "Terminal access is off", "Turn on CLI access for this deployment in the app.")
		return
	}
	if !signIn {
		message(w, http.StatusOK, "No sign-in needed", "This deployment doesn't use a browser sign-in, so the CLI connects without one.")
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	state := r.FormValue("state")
	if err != nil || (port != 0 && (port < 1024 || port > 65535)) || !cliStateRe.MatchString(state) {
		message(w, http.StatusBadRequest, "Bad request", "Start this from the supermatrix CLI: supermatrix login "+ent.Host)
		return
	}
	d := pageData{Title: "Allow terminal access", Host: ent.Host, Port: port, State: state, Who: who(s)}
	if r.Method != http.MethodPost {
		renderCSP(w, http.StatusOK, "cli", d, "form-action 'self' http://127.0.0.1:*")
		return
	}
	if !sameOrigin(r) {
		message(w, http.StatusForbidden, "Not allowed", "That form came from another site.")
		return
	}
	v := e.mintSession(ent, s.Kind, s.Email, s.Expires)
	if port == 0 {
		d.Title, d.Code = "Paste this into the terminal", v
		render(w, http.StatusOK, "cli-code", d)
		return
	}
	cb := fmt.Sprintf("http://127.0.0.1:%d/callback?state=%s&session=%s", port, url.QueryEscape(state), url.QueryEscape(v))
	http.Redirect(w, r, cb, http.StatusSeeOther)
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
func (e *Edge) sshPipe(w http.ResponseWriter, r *http.Request, ent *registry.Entry) {
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
	tunnel.Pipe(tunnel.NewWSConn(ws), up)
}

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
