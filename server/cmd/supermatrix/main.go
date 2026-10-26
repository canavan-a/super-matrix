// Command supermatrix is the client for SSH deployments: it signs in through
// the browser and carries the connection over the server's WebSocket, so ssh
// needs nothing but a ProxyCommand.
//
//	supermatrix access [--url URL] HOST            stdio pipe, for ssh's ProxyCommand
//	supermatrix forward [--url URL] [--listen ADDR] HOST   a local TCP port to HOST
//	supermatrix login [--url URL] [--no-browser] HOST       sign in and save the session
//	supermatrix logout HOST                        forget the saved session
//	supermatrix list                               saved sessions
//
// ~/.ssh/config:
//
//	Host ssh-box.example.com
//	  ProxyCommand supermatrix access %h
//
// Sessions are kept in $SUPERMATRIX_CONFIG_DIR (default ~/.config/supermatrix).
// For scripts and services, SUPERMATRIX_TOKEN_FILE (or SUPERMATRIX_TOKEN)
// is one of the deployment's API tokens (smt_…) and no sign-in is needed.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"main/tunnel"
	"main/version"
)

const usage = `supermatrix %s: reach Super Matrix SSH deployments

  supermatrix access [--url URL] HOST                    pipe stdin/stdout to HOST (ssh ProxyCommand)
  supermatrix forward [--url URL] [--listen ADDR] HOST   serve HOST on a local port (default 127.0.0.1:2222)
  supermatrix login [--url URL] [--no-browser] HOST      sign in through the browser and save the session
  supermatrix logout HOST                                forget HOST's session
  supermatrix list                                       saved sessions
  supermatrix version

In ~/.ssh/config:

  Host ssh-box.example.com
    ProxyCommand supermatrix access %%h

--url is the deployment's URL when it isn't https://HOST (e.g. http://box.localhost:8080 in dev).
SUPERMATRIX_TOKEN_FILE or SUPERMATRIX_TOKEN: an API token (smt_…) to use instead of signing in.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("supermatrix: ")
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version.Version)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "access":
		err = access(ctx, args)
	case "forward":
		err = forward(ctx, args)
	case "login":
		err = login(ctx, args)
	case "logout":
		err = logout(args)
	case "list":
		err = list()
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version.Version)
	default:
		fmt.Fprintf(os.Stderr, usage, version.Version)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// target is a deployment's base URL, e.g. https://ssh-box.example.com.
type target struct{ base *url.URL }

// parseTarget reads the flags shared by every command and the HOST
// argument; extra adds command-specific flags.
func parseTarget(name string, args []string, extra func(*flag.FlagSet)) (target, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	raw := fs.String("url", os.Getenv("SUPERMATRIX_URL"), "the deployment's URL when it isn't https://HOST")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return target{}, err
	}
	if fs.NArg() != 1 && *raw == "" {
		return target{}, fmt.Errorf("usage: supermatrix %s [flags] HOST", name)
	}
	s := *raw
	if s == "" {
		s = "https://" + fs.Arg(0)
	} else if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return target{}, fmt.Errorf("bad URL %q", s)
	}
	if u.Scheme == "http" && !isLocal(u.Hostname()) {
		// Sessions and tokens would cross the network in the clear.
		return target{}, fmt.Errorf("refusing plain http to %s: use https (http is only for localhost in dev)", u.Host)
	}
	return target{base: &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}}, nil
}

func (t target) key() string         { return t.base.Host }
func (t target) url(p string) string { return t.base.String() + p }

func (t target) cookieName() string {
	if t.base.Scheme == "https" {
		return "__Host-supermatrix"
	}
	return "supermatrix"
}

func (t target) wsURL() string {
	u := *t.base
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	u.Path = "/_supermatrix/ssh"
	return u.String()
}

// --- sessions

type savedSession struct {
	Value   string    `json:"value"`
	Expires time.Time `json:"expires"`
}

func configDir() (string, error) {
	if d := os.Getenv("SUPERMATRIX_CONFIG_DIR"); d != "" {
		return d, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "supermatrix"), nil
}

func loadSessions() (map[string]savedSession, error) {
	m := map[string]savedSession{}
	d, err := configDir()
	if err != nil {
		return m, err
	}
	b, err := os.ReadFile(filepath.Join(d, "sessions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func saveSessions(m map[string]savedSession) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(d, ".sessions.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(d, "sessions.json"))
}

// sessionExpiry reads the expiry out of a session value:
// base64("<deployment>|<unix expiry>|<kind>[|email]") "." mac.
func sessionExpiry(v string) (time.Time, error) {
	enc, _, ok := strings.Cut(v, ".")
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	parts := strings.Split(string(raw), "|")
	if !ok || err != nil || len(parts) < 3 {
		return time.Time{}, errors.New("that doesn't look like a Super Matrix session")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, errors.New("that doesn't look like a Super Matrix session")
	}
	return time.Unix(exp, 0), nil
}

func token() (string, error) {
	if f := os.Getenv("SUPERMATRIX_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("reading SUPERMATRIX_TOKEN_FILE: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv("SUPERMATRIX_TOKEN"), nil
}

// --- connecting

// errSignIn means the deployment wants a (new) sign-in.
var errSignIn = errors.New("sign-in needed")

// dial opens the pipe to t's target with the saved session or the token.
func dial(ctx context.Context, t target) (net.Conn, error) {
	h := http.Header{"User-Agent": {"supermatrix/" + version.Version}}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	if tok != "" {
		h.Set("X-Supermatrix-Token", tok)
	} else if m, _ := loadSessions(); sessionRe.MatchString(m[t.key()].Value) {
		h.Set("Cookie", t.cookieName()+"="+m[t.key()].Value)
	}
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 30 * time.Second, NetDialContext: dialLocal}
	ws, res, err := d.DialContext(ctx, t.wsURL(), h)
	if err == nil {
		return tunnel.NewWSConn(ws), nil
	}
	if res == nil {
		return nil, err
	}
	defer res.Body.Close()
	var body struct {
		Error string `json:"error"`
		Login string `json:"login"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	_ = json.Unmarshal(b, &body)
	switch {
	case res.StatusCode == http.StatusUnauthorized && tok == "" && body.Login != "":
		return nil, errSignIn
	case res.StatusCode == http.StatusUnauthorized && tok != "":
		return nil, fmt.Errorf("%s doesn't accept that API token (%s)", t.key(), res.Header.Get("X-Supermatrix-Auth"))
	case res.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%s only takes API tokens: set SUPERMATRIX_TOKEN_FILE", t.key())
	case res.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%s: not allowed from this address", t.key())
	case res.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s isn't an SSH deployment with CLI access on", t.key())
	case body.Error != "":
		return nil, fmt.Errorf("%s: %s", t.key(), body.Error)
	}
	return nil, fmt.Errorf("%s: %s", t.key(), res.Status)
}

func access(ctx context.Context, args []string) error {
	t, err := parseTarget("access", args, nil)
	if err != nil {
		return err
	}
	c, err := dial(ctx, t)
	if errors.Is(err, errSignIn) {
		if err = browserLogin(ctx, t); err != nil {
			return err
		}
		c, err = dial(ctx, t)
	}
	if err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(c, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, c); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return c.Close()
}

func forward(ctx context.Context, args []string) error {
	var listen string
	t, err := parseTarget("forward", args, func(fs *flag.FlagSet) {
		fs.StringVar(&listen, "listen", envOr("SUPERMATRIX_LISTEN", "127.0.0.1:2222"), "local address to listen on")
	})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("%s → %s", ln.Addr(), t.key())
	if ap, err := netip.ParseAddrPort(ln.Addr().String()); err == nil && !ap.Addr().IsLoopback() {
		log.Printf("warning: %s is reachable from other machines, and anyone who can reach it gets this deployment's ssh port", ln.Addr())
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		lc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			c, err := dial(ctx, t)
			if errors.Is(err, errSignIn) {
				err = fmt.Errorf("%s wants a sign-in: run `supermatrix login %s`, or set SUPERMATRIX_TOKEN_FILE", t.key(), t.key())
			}
			if err != nil {
				log.Print(err)
				lc.Close()
				return
			}
			tunnel.Pipe(lc, c)
		}()
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// --- signing in

func login(ctx context.Context, args []string) error {
	var noBrowser bool
	t, err := parseTarget("login", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&noBrowser, "no-browser", false, "print a URL to open anywhere and paste back what it shows")
	})
	if err != nil {
		return err
	}
	if noBrowser {
		return pasteLogin(ctx, t)
	}
	return browserLogin(ctx, t)
}

func newState() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkce is a fresh verifier and its challenge, base64url(SHA-256(verifier)):
// the consent page's code only works with the verifier, which never leaves
// this process.
func pkce() (verifier, challenge string) {
	verifier = newState()
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:])
}

func (t target) consentURL(port int, state, challenge string) string {
	q := url.Values{"port": {strconv.Itoa(port)}, "state": {state}, "challenge": {challenge}}
	return t.url("/_supermatrix/cli?" + q.Encode())
}

// sessionRe is what a session value can contain (base64url and the dot),
// so a bad one can't smuggle anything into a header.
var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// exchange trades a consent code (and the verifier) for the session, over
// the same HTTPS as everything else, and saves it.
func exchange(ctx context.Context, t target, code, verifier string) error {
	body := url.Values{"code": {code}, "verifier": {verifier}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url("/_supermatrix/cli/exchange"), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "supermatrix/"+version.Version)
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: dialLocal},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var out struct {
		Session string `json:"session"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	if res.StatusCode != http.StatusOK {
		if out.Error == "" {
			out.Error = res.Status
		}
		return fmt.Errorf("%s: %s", t.key(), out.Error)
	}
	return store(t, out.Session)
}

func store(t target, v string) error {
	if !sessionRe.MatchString(v) {
		return errors.New("that doesn't look like a Super Matrix session")
	}
	exp, err := sessionExpiry(v)
	if err != nil {
		return err
	}
	m, err := loadSessions()
	if err != nil {
		return err
	}
	m[t.key()] = savedSession{Value: v, Expires: exp}
	if err := saveSessions(m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Signed in to %s until %s.\n", t.key(), exp.Local().Format("Jan 2 15:04"))
	return nil
}

// browserLogin opens the deployment's consent page, which sends a code to a
// listener on loopback; the code and the verifier buy the session.
func browserLogin(ctx context.Context, t target) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	state := newState()
	verifier, challenge := pkce()
	got := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Super Matrix</title><body style="font-family:sans-serif;text-align:center;padding:48px">Done. You can close this tab and go back to the terminal.</body>`)
		select {
		case got <- r.URL.Query().Get("code"):
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	u := t.consentURL(ln.Addr().(*net.TCPAddr).Port, state, challenge)
	fmt.Fprintf(os.Stderr, "Sign in to %s in your browser:\n  %s\n(No browser on this machine? Run `supermatrix login --no-browser %s` first.)\n", t.key(), u, t.key())
	openBrowser(u)
	select {
	case code := <-got:
		return exchange(ctx, t, code, verifier)
	case <-time.After(5 * time.Minute):
		return errors.New("gave up waiting for the browser sign-in")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pasteLogin is for machines without a browser: the consent page shows the
// code instead of redirecting, and it's pasted here.
func pasteLogin(ctx context.Context, t target) error {
	verifier, challenge := pkce()
	fmt.Fprintf(os.Stderr, "Open this anywhere, sign in, and paste the code it shows:\n  %s\n> ", t.consentURL(0, newState(), challenge))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	return exchange(ctx, t, strings.TrimSpace(line), verifier)
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		c = exec.Command("xdg-open", u)
	}
	c.Stdout, c.Stderr = nil, nil
	_ = c.Start()
}

func logout(args []string) error {
	t, err := parseTarget("logout", args, nil)
	if err != nil {
		return err
	}
	m, err := loadSessions()
	if err != nil {
		return err
	}
	delete(m, t.key())
	return saveSessions(m)
}

func list() error {
	m, err := loadSessions()
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		state := "until " + m[h].Expires.Local().Format("Jan 2 15:04")
		if time.Now().After(m[h].Expires) {
			state = "expired"
		}
		fmt.Printf("%s\t%s\n", h, state)
	}
	return nil
}

// dialLocal resolves *.localhost to loopback (RFC 6761), as browsers do, so
// the dev server's <name>.localhost deployments work.
func dialLocal(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if isLocal(host) && net.ParseIP(host) == nil {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// isLocal reports whether host is this machine: localhost, *.localhost or a
// loopback address.
func isLocal(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
