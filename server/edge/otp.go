package edge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"math/big"
	"net/http"
	netmail "net/mail"
	"strings"
	"sync"
	"time"

	"main/registry"
)

// Mailer sends sign-in codes (main/mail in the server).
type Mailer interface {
	// Ready reports whether codes can go out at all.
	Ready() bool
	SendCode(to, host, code string) error
}

const (
	codeTTL      = 10 * time.Minute
	codeAttempts = 5
)

// otpCode is a code waiting to be entered. Only its hash is kept, and only
// in memory: a restart just means asking for a new one.
type otpCode struct {
	hash    [32]byte
	expires time.Time
	tries   int
}

type otpStore struct {
	mu    sync.Mutex
	codes map[string]*otpCode // otpKey(deployment, email)
}

func otpKey(ent *registry.Entry, email string) string { return ent.ID + "\x00" + email }

func hashCode(key, code string) [32]byte { return sha256.Sum256([]byte(key + "\x00" + code)) }

// newCode makes a 6-digit code for key, replacing any earlier one.
func (s *otpStore) newCode(key string, now time.Time) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.codes) > 10000 {
		for k, c := range s.codes {
			if now.After(c.expires) {
				delete(s.codes, k)
			}
		}
	}
	s.codes[key] = &otpCode{hash: hashCode(key, code), expires: now.Add(codeTTL)}
	return code, nil
}

// check uses up a code if it matches. A code dies after codeAttempts wrong
// guesses or codeTTL.
func (s *otpStore) check(key, code string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.codes[key]
	if c == nil {
		return false
	}
	if now.After(c.expires) {
		delete(s.codes, key)
		return false
	}
	h := hashCode(key, code)
	if subtle.ConstantTimeCompare(h[:], c.hash[:]) == 1 {
		delete(s.codes, key)
		return true
	}
	c.tries++
	if c.tries >= codeAttempts {
		delete(s.codes, key)
	}
	return false
}

// NormalizeEmail lowercases and checks an address; "" if it isn't one.
func NormalizeEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 || strings.ContainsAny(s, "|\n\r") {
		return ""
	}
	a, err := netmail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(s, "@") {
		return ""
	}
	return s
}

// otpRoutes serves the email-code sign-in: enter an address, get a code by
// email if it's on the list, enter the code, get a session cookie for as
// long as the deployment's session length.
func (e *Edge) otpRoutes(w http.ResponseWriter, r *http.Request, ent *registry.Entry, ip string) {
	next := safeNext(r.FormValue("next"))
	page := func(status int, name string, d pageData) {
		d.Title, d.Host, d.Next = "Sign in", ent.Host, next
		render(w, status, name, d)
	}
	switch {
	case r.URL.Path == "/_supermatrix/login" && r.Method != http.MethodPost:
		if _, state := e.session(r, ent); state == authOK {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		page(http.StatusOK, "otp-email", pageData{Notice: expiredNotice(r)})

	case r.URL.Path == "/_supermatrix/login":
		email := NormalizeEmail(r.PostFormValue("email"))
		if email == "" {
			page(http.StatusBadRequest, "otp-email", pageData{Error: "Enter a valid email address."})
			return
		}
		if e.Mail == nil || !e.Mail.Ready() {
			page(http.StatusServiceUnavailable, "otp-email", pageData{Email: email, Error: "Email sign-in isn't set up on this server yet."})
			return
		}
		now := e.now()
		if !e.sendIPLimit.Allow(ip, now) || !e.sendEmailLimit.Allow(otpKey(ent, email), now) || !e.sendAllLimit.Allow("all", now) {
			page(http.StatusTooManyRequests, "otp-email", pageData{Email: email, Error: "Too many codes asked for. Wait a few minutes and try again."})
			return
		}
		// The same answer either way, and the email goes out in the
		// background, so this can't be used to find out who's on the list.
		if ent.AllowsEmail(email) {
			code, err := e.otp.newCode(otpKey(ent, email), now)
			if err != nil {
				message(w, http.StatusInternalServerError, "Something went wrong", "Couldn't make a code. Try again.")
				return
			}
			go func() {
				if err := e.Mail.SendCode(email, ent.Host, code); err != nil {
					log.Printf("edge: sign-in code for %s on %s: %v", email, ent.Host, err)
				}
			}()
		}
		page(http.StatusOK, "otp-code", pageData{Email: email})

	case r.URL.Path == "/_supermatrix/verify" && r.Method == http.MethodPost:
		email := NormalizeEmail(r.PostFormValue("email"))
		code := strings.Join(strings.Fields(r.PostFormValue("code")), "")
		if email == "" {
			http.Redirect(w, r, loginURL(next, false), http.StatusSeeOther)
			return
		}
		if !e.loginLimit.Allow(ip, e.now()) {
			page(http.StatusTooManyRequests, "otp-code", pageData{Email: email, Error: "Too many attempts. Wait a minute and try again."})
			return
		}
		if !ent.AllowsEmail(email) || !e.otp.check(otpKey(ent, email), code, e.now()) {
			page(http.StatusUnauthorized, "otp-code", pageData{Email: email, Error: "That code is wrong or has expired."})
			return
		}
		http.SetCookie(w, e.makeCookie(ent, "o", email))
		http.Redirect(w, r, next, http.StatusSeeOther)

	default:
		message(w, http.StatusNotFound, "Not found", "Nothing here.")
	}
}
