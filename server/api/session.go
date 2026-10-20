package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"main/database"
	"main/edge"
)

const sessionTTL = 30 * 24 * time.Hour

func bearer(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// login trades the root password for a session token.
func (a *API) login(c *gin.Context) {
	if !a.loginLimit.Allow(a.clientIP(c), a.now()) {
		abort(c, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	var b struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&b); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if !edge.PasswordEqual(b.Password, a.cfg.RootPassword) {
		abort(c, http.StatusUnauthorized, "wrong password")
		return
	}
	tok := database.NewSecret("sms")
	now := a.now()
	ua := c.Request.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	s := database.AdminSession{
		TokenHash: database.HashSecret(tok), PasswordFP: edge.Fingerprint(a.cfg.RootPassword),
		CreatedAt: now, ExpiresAt: now.Add(sessionTTL), UserAgent: ua,
	}
	if err := a.db.Create(&s).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	// Opportunistic cleanup.
	a.db.Where("expires_at < ?", now).Delete(&database.AdminSession{})
	c.JSON(http.StatusOK, gin.H{"token": tok, "expiresAt": s.ExpiresAt})
}

// validSession reports whether tok is a live admin session.
func (a *API) validSession(tok string) bool {
	if tok == "" {
		return false
	}
	s, err := database.First[database.AdminSession](a.db.Where("token_hash = ?", database.HashSecret(tok)))
	return err == nil && s != nil && a.now().Before(s.ExpiresAt) && s.PasswordFP == edge.Fingerprint(a.cfg.RootPassword)
}

func (a *API) requireAdmin(c *gin.Context) {
	if !a.validSession(bearer(c.Request)) {
		abort(c, http.StatusUnauthorized, "sign in again")
		return
	}
	c.Next()
}

func (a *API) logout(c *gin.Context) {
	a.db.Where("token_hash = ?", database.HashSecret(bearer(c.Request))).Delete(&database.AdminSession{})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
