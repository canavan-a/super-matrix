package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"main/database"
)

// API tokens (sma_…) let automation, like CI applying a schema, call the
// routes their scope allows without the root password. They're made and
// revoked by the admin, shown once, and survive a root password change.

// knownScopes is every scope this server understands; a token with any
// other (made by a newer server, say) is refused.
var knownScopes = []string{database.ScopeSchema}

type apiTokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func apiTokenOut(t database.APIToken) apiTokenDTO {
	return apiTokenDTO{ID: t.ID, Name: t.Name, Scope: t.Scope, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

func (a *API) listAPITokens(c *gin.Context) {
	var ts []database.APIToken
	if err := a.db.Order("created_at").Find(&ts).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]apiTokenDTO, 0, len(ts))
	for _, t := range ts {
		out = append(out, apiTokenOut(t))
	}
	c.JSON(http.StatusOK, out)
}

// createAPIToken makes a token and returns it, the only time it's shown.
func (a *API) createAPIToken(c *gin.Context) {
	var b struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&b); err != nil || cleanName(b.Name) == "" {
		abort(c, http.StatusBadRequest, "name required")
		return
	}
	if b.Scope == "" {
		b.Scope = database.ScopeSchema
	}
	if !slices.Contains(knownScopes, b.Scope) {
		abort(c, http.StatusBadRequest, "scope must be one of: "+strings.Join(knownScopes, ", "))
		return
	}
	tok := database.NewSecret("sma")
	t := database.APIToken{ID: database.NewID(), Name: cleanName(b.Name), TokenHash: database.HashSecret(tok), Scope: b.Scope, CreatedAt: a.now()}
	if err := a.db.Create(&t).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"apiToken": apiTokenOut(t), "token": tok})
}

func (a *API) deleteAPIToken(c *gin.Context) {
	res := a.db.Where("id = ?", c.Param("id")).Delete(&database.APIToken{})
	if res.Error != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if res.RowsAffected == 0 {
		abort(c, http.StatusNotFound, "no such API token")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

const ctxActor = "actor"

// requireScope lets through the admin (a session) or an API token with
// scope. It records who's asking, for schema_state.applied_by.
func (a *API) requireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := bearer(c.Request)
		if strings.HasPrefix(tok, "sma_") {
			t, err := database.First[database.APIToken](a.db.Where("token_hash = ?", database.HashSecret(tok)))
			if err != nil {
				abort(c, http.StatusInternalServerError, "server error")
				return
			}
			if t == nil {
				abort(c, http.StatusUnauthorized, "unknown API token")
				return
			}
			if t.Scope != scope || !slices.Contains(knownScopes, t.Scope) {
				abort(c, http.StatusForbidden, "this API token can't do that")
				return
			}
			now := a.now()
			if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
				a.db.Model(t).Update("last_used_at", now)
			}
			c.Set(ctxActor, "token:"+t.Name)
			c.Next()
			return
		}
		if !a.validSession(tok) {
			abort(c, http.StatusUnauthorized, "sign in again")
			return
		}
		c.Set(ctxActor, "admin")
		c.Next()
	}
}

func actorOf(c *gin.Context) string {
	if v, ok := c.Get(ctxActor); ok {
		return v.(string)
	}
	return "admin"
}
