package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"main/database"
	"main/registry"
)

// UserHeader tells the service behind an email-code deployment who signed
// in. Whatever a client sends in it is dropped first.
const UserHeader = "X-Supermatrix-User"

// AuthHeader is on every 401 the edge sends for a deployment: "required"
// (no credentials) or "expired" (a session or token that ran out), so a
// front end or script can tell "sign in again" from "never signed in".
const AuthHeader = "X-Supermatrix-Auth"

// Auth states.
const (
	authOK       = "ok"
	authRequired = "required"
	authExpired  = "expired"
)

// cookieGrace keeps the cookie in the browser past its session's expiry,
// so a request after it can be told apart from one that never signed in.
// The signed expiry inside the cookie is what counts.
const cookieGrace = 7 * 24 * time.Hour

// LoadKey reads the cookie-signing key at path, making it on first start.
func LoadKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0o600)
}

// PasswordEqual compares two passwords in constant time.
func PasswordEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// Fingerprint identifies a password without revealing it (sessions are tied
// to the one they were made with).
func Fingerprint(pw string) string {
	h := sha256.Sum256([]byte("supermatrix-root\x00" + pw))
	return hex.EncodeToString(h[:8])
}

func (e *Edge) cookieName() string {
	// __Host- pins the cookie to exactly this host over https, so a
	// deployment can't set or read another deployment's session.
	if e.cfg.TLSOn() {
		return "__Host-supermatrix"
	}
	return "supermatrix"
}

// Session cookies: "<deployment>|<expiry>|<kind>[|<email>]" plus an HMAC
// over that and what it was made with. kind is "d" (deployment password),
// "r" (root password) or "o" (an emailed code, with the address), so
// changing a password signs those sessions out, as does taking an address
// off the list.
func (e *Edge) sessionEpoch(ent *registry.Entry, kind, email string) string {
	switch kind {
	case "r":
		return Fingerprint(e.cfg.RootPassword)
	case "o":
		return "otp\x00" + email
	}
	return ent.LoginPassHash
}

func (e *Edge) sign(payload, epoch string) string {
	m := hmac.New(sha256.New, e.key)
	m.Write([]byte(payload + "|" + epoch))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func sessionTTL(ent *registry.Entry) time.Duration {
	if ttl := time.Duration(ent.SessionTTLS) * time.Second; ttl > 0 {
		return ttl
	}
	return 7 * 24 * time.Hour
}

func (e *Edge) makeCookie(ent *registry.Entry, kind, email string) *http.Cookie {
	exp := e.now().Add(sessionTTL(ent))
	payload := ent.ID + "|" + strconv.FormatInt(exp.Unix(), 10) + "|" + kind
	if kind == "o" {
		payload += "|" + email
	}
	v := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + e.sign(payload, e.sessionEpoch(ent, kind, email))
	keep := sessionTTL(ent) + cookieGrace
	return &http.Cookie{
		Name: e.cookieName(), Value: v, Path: "/", Expires: exp.Add(cookieGrace), MaxAge: int(keep.Seconds()),
		HttpOnly: true, Secure: e.cfg.TLSOn(), SameSite: http.SameSiteLaxMode,
	}
}

func (e *Edge) clearCookie() *http.Cookie {
	return &http.Cookie{Name: e.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: e.cfg.TLSOn(), SameSite: http.SameSiteLaxMode}
}

// session is a signed-in visitor.
type session struct {
	Email   string // otp sessions only
	Expires time.Time
}

// session checks the request's cookie against ent: authOK, authExpired (a
// genuine session that ran out) or authRequired (none, or not valid here).
func (e *Edge) session(r *http.Request, ent *registry.Entry) (session, string) {
	c, err := r.Cookie(e.cookieName())
	if err != nil {
		return session{}, authRequired
	}
	enc, mac, ok := strings.Cut(c.Value, ".")
	if !ok {
		return session{}, authRequired
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return session{}, authRequired
	}
	payload := string(raw)
	parts := strings.Split(payload, "|")
	if len(parts) < 3 || parts[0] != ent.ID {
		return session{}, authRequired
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return session{}, authRequired
	}
	kind, email := parts[2], ""
	switch {
	case kind == "d" && len(parts) == 3 && ent.AuthMode == database.AuthLogin:
		if ent.LoginPassHash == "" {
			return session{}, authRequired
		}
	case kind == "r" && len(parts) == 3 && ent.AuthMode == database.AuthLogin:
		if !ent.AllowRootLogin {
			return session{}, authRequired
		}
	case kind == "o" && len(parts) == 4 && ent.AuthMode == database.AuthOTP:
		email = parts[3]
		if !ent.OTPEmails[email] {
			return session{}, authRequired
		}
	default:
		return session{}, authRequired
	}
	want := e.sign(payload, e.sessionEpoch(ent, kind, email))
	if !hmac.Equal([]byte(mac), []byte(want)) {
		return session{}, authRequired
	}
	s := session{Email: email, Expires: time.Unix(exp, 0)}
	if !e.now().Before(s.Expires) {
		return s, authExpired
	}
	return s, authOK
}

// stripCookie removes our session cookie before the request goes upstream.
func (e *Edge) stripCookie(r *http.Request) {
	cs := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name != e.cookieName() {
			r.AddCookie(c)
		}
	}
}

func wantsHTML(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func loginURL(next string, expired bool) string {
	u := "/_supermatrix/login?next=" + url.QueryEscape(next)
	if expired {
		u += "&expired=1"
	}
	return u
}

// signInRequired sends a page load to the sign-in page and answers anything
// else (fetch/XHR, scripts) with a 401 a front end can act on.
func (e *Edge) signInRequired(w http.ResponseWriter, r *http.Request, state string) {
	login := loginURL(r.URL.RequestURI(), state == authExpired)
	if wantsHTML(r) {
		http.Redirect(w, r, login, http.StatusSeeOther)
		return
	}
	msg := "sign in required"
	if state == authExpired {
		msg = "session expired"
	}
	w.Header().Set(AuthHeader, state)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msg, "auth": state, "login": login})
}

// authorize enforces ent's auth mode. It writes the response and returns
// false when the request may not go through.
func (e *Edge) authorize(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) bool {
	switch ent.AuthMode {
	case database.AuthNone:
		return true

	case database.AuthLogin, database.AuthOTP:
		s, state := e.session(r, ent)
		if state == authOK {
			e.stripCookie(r)
			if s.Email != "" {
				r.Header.Set(UserHeader, s.Email)
			}
			return true
		}
		e.signInRequired(w, r, state)
		return false

	case database.AuthBasic:
		if u, p, ok := r.BasicAuth(); ok {
			// Only failures count toward the limit; a signed-in browser
			// sends credentials with every request.
			if e.basicCached(ent, u, p) {
				r.Header.Del("Authorization")
				return true
			}
			if e.failLimit.Blocked(ip, e.now()) {
				http.Error(w, "too many attempts", http.StatusTooManyRequests)
				return false
			}
			if e.checkBasic(ent, u, p) {
				r.Header.Del("Authorization")
				return true
			}
			e.failLimit.Allow(ip, e.now())
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Super Matrix", charset="UTF-8"`)
		w.Header().Set(AuthHeader, authRequired)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false

	case database.AuthToken:
		tok := r.Header.Get("X-Supermatrix-Token")
		if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tok == "" {
			tok = strings.TrimSpace(b)
		}
		if tok != "" {
			if t, ok := ent.Tokens[database.HashSecret(tok)]; ok {
				if !t.ExpiresAt.IsZero() && !e.now().Before(t.ExpiresAt) {
					w.Header().Set("WWW-Authenticate", `Bearer realm="Super Matrix", error="invalid_token", error_description="token expired"`)
					w.Header().Set(AuthHeader, authExpired)
					http.Error(w, "token expired", http.StatusUnauthorized)
					return false
				}
				r.Header.Del("Authorization")
				r.Header.Del("X-Supermatrix-Token")
				if e.TokenUsed != nil {
					e.TokenUsed(t.ID)
				}
				return true
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="Super Matrix"`)
		w.Header().Set(AuthHeader, authRequired)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	http.Error(w, "misconfigured deployment", http.StatusForbidden)
	return false
}

// basicCache remembers recent successful basic-auth checks: bcrypt on every
// request would cost ~50ms each.
type basicCache struct {
	mu sync.Mutex
	ok map[string]time.Time
}

func basicKey(ent *registry.Entry, user, pass string) string {
	h := sha256.Sum256([]byte(ent.ID + "\x00" + ent.BasicPassHash + "\x00" + user + "\x00" + pass))
	return string(h[:])
}

func (e *Edge) basicCached(ent *registry.Entry, user, pass string) bool {
	e.basic.mu.Lock()
	until, hit := e.basic.ok[basicKey(ent, user, pass)]
	e.basic.mu.Unlock()
	return hit && e.now().Before(until)
}

func (e *Edge) checkBasic(ent *registry.Entry, user, pass string) bool {
	if ent.BasicUser == "" || ent.BasicPassHash == "" {
		return false
	}
	now := e.now()
	key := basicKey(ent, user, pass)
	if subtle.ConstantTimeCompare([]byte(user), []byte(ent.BasicUser)) != 1 ||
		bcrypt.CompareHashAndPassword([]byte(ent.BasicPassHash), []byte(pass)) != nil {
		return false
	}
	e.basic.mu.Lock()
	if len(e.basic.ok) > 10000 {
		e.basic.ok = map[string]time.Time{}
	}
	e.basic.ok[key] = now.Add(10 * time.Minute)
	e.basic.mu.Unlock()
	return true
}

// safeNext keeps post-login redirects on this host.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || strings.HasPrefix(next, "/_supermatrix/") {
		return "/"
	}
	return next
}

// loginRoutes serves /_supermatrix/* on deployments in login or otp mode.
func (e *Edge) loginRoutes(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) {
	switch r.URL.Path {
	case "/_supermatrix/session":
		// For front ends: who's signed in and until when, or why not.
		s, state := e.session(r, ent)
		if state != authOK {
			w.Header().Set(AuthHeader, state)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"signedIn": false, "auth": state, "login": loginURL("/", state == authExpired)})
			return
		}
		out := map[string]any{"signedIn": true, "expiresAt": s.Expires.UTC()}
		if s.Email != "" {
			out["email"] = s.Email
		}
		writeJSON(w, http.StatusOK, out)
		return
	case "/_supermatrix/logout":
		http.SetCookie(w, e.clearCookie())
		http.Redirect(w, r, "/_supermatrix/login", http.StatusSeeOther)
		return
	}
	if ent.AuthMode == database.AuthOTP {
		e.otpRoutes(w, r, ent, ip)
		return
	}
	switch r.URL.Path {
	case "/_supermatrix/login":
		next := safeNext(r.FormValue("next"))
		notice := expiredNotice(r)
		if r.Method != http.MethodPost {
			if _, state := e.session(r, ent); state == authOK {
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
			render(w, http.StatusOK, "login", pageData{Title: "Sign in", Host: ent.Host, Next: next, Notice: notice})
			return
		}
		if !e.loginLimit.Allow(ip, e.now()) {
			render(w, http.StatusTooManyRequests, "login", pageData{Title: "Sign in", Host: ent.Host, Next: next, Error: "Too many attempts. Wait a minute and try again."})
			return
		}
		pw := r.PostFormValue("password")
		kind := ""
		switch {
		case ent.LoginPassHash != "" && bcrypt.CompareHashAndPassword([]byte(ent.LoginPassHash), []byte(pw)) == nil:
			kind = "d"
		case ent.AllowRootLogin && PasswordEqual(pw, e.cfg.RootPassword):
			kind = "r"
		}
		if kind == "" {
			render(w, http.StatusUnauthorized, "login", pageData{Title: "Sign in", Host: ent.Host, Next: next, Error: "Wrong password."})
			return
		}
		http.SetCookie(w, e.makeCookie(ent, kind, ""))
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		message(w, http.StatusNotFound, "Not found", "Nothing here.")
	}
}

func expiredNotice(r *http.Request) string {
	if r.Method != http.MethodPost && r.FormValue("expired") == "1" {
		return "Your session expired. Sign in again."
	}
	return ""
}
