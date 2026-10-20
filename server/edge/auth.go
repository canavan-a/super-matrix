package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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

// Session cookies: "<deployment>|<expiry>|<kind>" plus an HMAC over that and
// the password it was made with. kind is "d" (deployment password) or "r"
// (root password), so changing either one signs those sessions out.
func (e *Edge) sessionEpoch(ent *registry.Entry, kind string) string {
	if kind == "r" {
		return Fingerprint(e.cfg.RootPassword)
	}
	return ent.LoginPassHash
}

func (e *Edge) sign(payload, epoch string) string {
	m := hmac.New(sha256.New, e.key)
	m.Write([]byte(payload + "|" + epoch))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (e *Edge) makeCookie(ent *registry.Entry, kind string) *http.Cookie {
	ttl := time.Duration(ent.SessionTTLS) * time.Second
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	exp := e.now().Add(ttl)
	payload := ent.ID + "|" + strconv.FormatInt(exp.Unix(), 10) + "|" + kind
	v := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + e.sign(payload, e.sessionEpoch(ent, kind))
	return &http.Cookie{
		Name: e.cookieName(), Value: v, Path: "/", Expires: exp, MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: e.cfg.TLSOn(), SameSite: http.SameSiteLaxMode,
	}
}

func (e *Edge) validCookie(r *http.Request, ent *registry.Entry) bool {
	c, err := r.Cookie(e.cookieName())
	if err != nil {
		return false
	}
	enc, mac, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	payload := string(raw)
	parts := strings.Split(payload, "|")
	if len(parts) != 3 || parts[0] != ent.ID {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || e.now().Unix() > exp {
		return false
	}
	switch parts[2] {
	case "d":
		if ent.LoginPassHash == "" {
			return false
		}
	case "r":
		if !ent.AllowRootLogin {
			return false
		}
	default:
		return false
	}
	want := e.sign(payload, e.sessionEpoch(ent, parts[2]))
	return hmac.Equal([]byte(mac), []byte(want))
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

// authorize enforces ent's auth mode. It writes the response and returns
// false when the request may not go through.
func (e *Edge) authorize(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) bool {
	switch ent.AuthMode {
	case database.AuthNone:
		return true

	case database.AuthLogin:
		if e.validCookie(r, ent) {
			e.stripCookie(r)
			return true
		}
		if wantsHTML(r) {
			http.Redirect(w, r, "/_supermatrix/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return false
		}
		http.Error(w, "sign in required", http.StatusUnauthorized)
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
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false

	case database.AuthToken:
		tok := r.Header.Get("X-Supermatrix-Token")
		if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tok == "" {
			tok = strings.TrimSpace(b)
		}
		if tok != "" {
			if id, ok := ent.Tokens[database.HashSecret(tok)]; ok {
				r.Header.Del("Authorization")
				r.Header.Del("X-Supermatrix-Token")
				if e.TokenUsed != nil {
					e.TokenUsed(id)
				}
				return true
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="Super Matrix"`)
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

// loginRoutes serves /_supermatrix/* on deployments in login mode.
func (e *Edge) loginRoutes(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) {
	switch r.URL.Path {
	case "/_supermatrix/login":
		next := safeNext(r.FormValue("next"))
		if r.Method != http.MethodPost {
			if e.validCookie(r, ent) {
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
			render(w, http.StatusOK, "login", pageData{Title: "Sign in", Host: ent.Host, Next: next})
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
		http.SetCookie(w, e.makeCookie(ent, kind))
		http.Redirect(w, r, next, http.StatusSeeOther)
	case "/_supermatrix/logout":
		http.SetCookie(w, &http.Cookie{Name: e.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: e.cfg.TLSOn(), SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/_supermatrix/login", http.StatusSeeOther)
	default:
		message(w, http.StatusNotFound, "Not found", "Nothing here.")
	}
}
