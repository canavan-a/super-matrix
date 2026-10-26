package edge

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"main/registry"
)

// The browser shell: an xterm.js page behind the deployment's sign-in. The
// server is the SSH client: it asks for the target's own username and
// password in the terminal, connects through the relay, and keeps nothing.
// The target's host key is trusted the first time and checked after that.
//
// On the WebSocket, binary messages are terminal bytes both ways; text
// messages are JSON: {"type":"resize","cols","rows"} from the page and
// {"type":"exit","message"} from the server.

//go:embed assets
var assets embed.FS

// serveAsset serves the vendored xterm.js files and the shell's script.
func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.URL.Path)
	b, err := assets.ReadFile("assets/" + name)
	if err != nil || name == "XTERM-LICENSE" {
		http.NotFound(w, r)
		return
	}
	switch path.Ext(name) {
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

var shellTmpl = template.Must(template.New("shell").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>{{.Host}} · Super Matrix</title>
<link rel="stylesheet" href="/_supermatrix/assets/xterm.css"><link rel="stylesheet" href="/_supermatrix/assets/shell.css"></head>
<body><header><span class="brand">Super Matrix</span><span class="host">{{.Host}}</span><span id="status"></span>{{if .SignOut}}<a href="/_supermatrix/logout">Sign out</a>{{end}}</header>
<div id="term"></div>
<script src="/_supermatrix/assets/xterm.js"></script><script src="/_supermatrix/assets/addon-fit.js"></script><script src="/_supermatrix/assets/shell.js"></script></body></html>`))

func (e *Edge) shellPage(w http.ResponseWriter, ent *registry.Entry, signOut bool) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' wss://"+ent.Host+" ws://"+ent.Host+":*; frame-ancestors 'none'")
	_ = shellTmpl.Execute(w, pageData{Host: ent.Host, SignOut: signOut})
}

// term is the shell's WebSocket as a terminal: Read gets keystrokes, Write
// shows output, and resizes go to whatever OnResize is set to.
type term struct {
	ws  *websocket.Conn
	wmu sync.Mutex
	in  *io.PipeReader
	br  *bufio.Reader

	mu         sync.Mutex
	cols, rows int
	onResize   func(cols, rows int)
}

func newTerm(ws *websocket.Conn) *term {
	pr, pw := io.Pipe()
	t := &term{ws: ws, in: pr, cols: 80, rows: 24}
	t.br = bufio.NewReader(pr)
	go func() {
		for {
			mt, b, err := ws.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if mt == websocket.BinaryMessage {
				if _, err := pw.Write(b); err != nil {
					return
				}
				continue
			}
			var m struct {
				Type       string
				Cols, Rows int
			}
			if json.Unmarshal(b, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 1000 {
				t.mu.Lock()
				t.cols, t.rows = m.Cols, m.Rows
				f := t.onResize
				t.mu.Unlock()
				if f != nil {
					f(m.Cols, m.Rows)
				}
			}
		}
	}()
	return t
}

func (t *term) Write(p []byte) (int, error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := t.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *term) Read(p []byte) (int, error) { return t.br.Read(p) }

func (t *term) say(format string, a ...any) { _, _ = fmt.Fprintf(t, format, a...) }

func (t *term) exit(msg string) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	b, _ := json.Marshal(map[string]string{"type": "exit", "message": msg})
	_ = t.ws.WriteMessage(websocket.TextMessage, b)
}

func (t *term) size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

func (t *term) setResize(f func(cols, rows int)) {
	t.mu.Lock()
	t.onResize = f
	t.mu.Unlock()
}

var errCancelled = errors.New("cancelled")

// prompt reads a line, echoing it unless it's secret. Ctrl-C or Ctrl-D
// cancels.
func (t *term) prompt(label string, echo bool) (string, error) {
	t.say("%s", label)
	var line []rune
	for {
		c, _, err := t.br.ReadRune()
		if err != nil {
			return "", err
		}
		switch c {
		case '\r', '\n':
			t.say("\r\n")
			return string(line), nil
		case 3, 4:
			t.say("^C\r\n")
			return "", errCancelled
		case 127, 8:
			if len(line) > 0 {
				line = line[:len(line)-1]
				if echo {
					t.say("\b \b")
				}
			}
		case 27:
			// An escape sequence (arrow keys and the like): drop it.
			if b, _ := t.br.Peek(1); len(b) == 1 && (b[0] == '[' || b[0] == 'O') {
				_, _ = t.br.ReadByte()
				for {
					x, err := t.br.ReadByte()
					if err != nil || (x >= 0x40 && x <= 0x7e) {
						break
					}
				}
			}
		default:
			if c < 32 || len(line) >= 1024 {
				continue
			}
			line = append(line, c)
			if echo {
				t.say("%c", c)
			}
		}
	}
}

func (e *Edge) shellSocket(w http.ResponseWriter, r *http.Request, ent *registry.Entry, s session, orig *http.Request, ip string) {
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	// Keystrokes and resizes are small; nothing needs a big message.
	ws.SetReadLimit(64 << 10)
	t := newTerm(ws)
	defer t.in.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.watchAccess(ctx, orig, ent.ID, ip, true, func() {
		t.say("\r\n\x1b[33mAccess to %s changed; disconnected.\x1b[0m\r\n", ent.Host)
		t.exit("Access ended.")
		ws.Close()
	})
	if !s.Expires.IsZero() {
		// The shell ends with the Super Matrix session.
		stop := time.AfterFunc(time.Until(s.Expires), func() {
			t.say("\r\n\x1b[33mYour Super Matrix session ended.\x1b[0m\r\n")
			t.exit("Session ended. Sign in again to reconnect.")
			ws.Close()
		})
		defer stop.Stop()
	}
	msg := e.runShell(r, t, ent)
	t.exit(msg)
}

// runShell signs in to the target and runs a shell until it exits; it
// returns what to tell the page.
func (e *Edge) runShell(r *http.Request, t *term, ent *registry.Entry) string {
	user := ent.SSHUser
	label := "login as: "
	if user != "" {
		label = "login as [" + user + "]: "
	}
	typed, err := t.prompt(label, true)
	if err != nil {
		return "Cancelled."
	}
	if typed != "" {
		user = typed
	}
	if user == "" {
		t.say("A username is needed.\r\n")
		return "No username."
	}

	t.say("\x1b[2mConnecting to %s through the relay…\x1b[0m\r\n", ent.Host)
	up, err := e.hub.Dial(r.Context(), ent.RelayID, ent.ID)
	if err != nil {
		t.say("\x1b[31m%s\x1b[0m\r\n", capitalize(dialProblem(err)))
		return "Couldn't connect."
	}
	defer up.Close()

	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
				return t.prompt(user+"@"+ent.Host+"'s password: ", false)
			}), 3),
			ssh.RetryableAuthMethod(ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				if name != "" {
					t.say("%s\r\n", name)
				}
				if instruction != "" {
					t.say("%s\r\n", instruction)
				}
				answers := make([]string, len(questions))
				for i, q := range questions {
					a, err := t.prompt(q, echos[i])
					if err != nil {
						return nil, err
					}
					answers[i] = a
				}
				return answers, nil
			}), 3),
		},
		HostKeyCallback: e.hostKeyCheck(t, ent),
		Timeout:         30 * time.Second,
	}
	_ = up.SetDeadline(time.Now().Add(2 * time.Minute)) // the sign-in, typing included
	addr := net.JoinHostPort(ent.TargetHost, strconv.Itoa(ent.TargetPort))
	c, chans, reqs, err := ssh.NewClientConn(up, addr, cfg)
	if err != nil {
		if !errors.Is(err, errCancelled) {
			t.say("\x1b[31m%s\x1b[0m\r\n", sshProblem(err))
		}
		return "Not signed in."
	}
	_ = up.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.say("\x1b[31mCouldn't open a session: %v\x1b[0m\r\n", err)
		return "Couldn't open a session."
	}
	defer sess.Close()
	cols, rows := t.size()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		t.say("\x1b[31mCouldn't get a terminal: %v\x1b[0m\r\n", err)
		return "Couldn't get a terminal."
	}
	t.setResize(func(cols, rows int) { _ = sess.WindowChange(rows, cols) })
	sess.Stdin, sess.Stdout, sess.Stderr = t, t, t
	if err := sess.Shell(); err != nil {
		t.say("\x1b[31mCouldn't start a shell: %v\x1b[0m\r\n", err)
		return "Couldn't start a shell."
	}
	_ = sess.Wait()
	return "Connection closed."
}

// hostKeyCheck trusts the target's host key the first time and refuses a
// different one after that, until it's reset in the app.
func (e *Edge) hostKeyCheck(t *term, ent *registry.Entry) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		switch ent.SSHHostKey {
		case fp:
			return nil
		case "":
			t.say("\x1b[2mFirst connection: saved the host key (%s %s).\x1b[0m\r\n", key.Type(), fp)
			if e.HostKeySeen != nil {
				e.HostKeySeen(ent.ID, fp)
			}
			return nil
		}
		return &hostKeyError{want: ent.SSHHostKey, got: fp}
	}
}

type hostKeyError struct{ want, got string }

func (h *hostKeyError) Error() string {
	return "the host key changed: expected " + h.want + ", got " + h.got +
		". If the server was reinstalled, reset the host key in the app; otherwise something is in the way."
}

func sshProblem(err error) string {
	var hk *hostKeyError
	if errors.As(err, &hk) {
		return capitalize(hk.Error())
	}
	return "Couldn't sign in: " + err.Error()
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}
