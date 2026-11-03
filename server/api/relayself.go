package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
)

// A relay can manage its own deployments with its token, once that's
// turned on for it in the app (Relay.ManageDeployments). The routes under
// /v1/relay/ are the admin ones, scoped: a relay sees, changes and creates
// only deployments on itself, and can't move one to another relay.

const ctxRelay = "relay"

// requireRelay authenticates a relay by its token (Authorization: Bearer smr_…).
func (a *API) requireRelay(c *gin.Context) {
	r, err := a.relayByToken(bearer(c.Request))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if r == nil {
		abort(c, http.StatusUnauthorized, "unknown relay token")
		return
	}
	c.Set(ctxRelay, r)
	c.Next()
}

// requireManage lets through relays allowed to manage their deployments,
// and logs what they change.
func (a *API) requireManage(c *gin.Context) {
	r := relayOf(c)
	if !r.ManageDeployments {
		abort(c, http.StatusForbidden, "this relay isn't allowed to manage deployments; turn it on for "+r.Name+" in the app")
		return
	}
	c.Next()
	if c.Request.Method != http.MethodGet && c.Writer.Status() < 300 {
		log.Printf("relay %q: %s %s", r.Name, c.Request.Method, c.Request.URL.Path)
	}
}

// relayOf is the relay making the request, nil for the admin.
func relayOf(c *gin.Context) *database.Relay {
	if v, ok := c.Get(ctxRelay); ok {
		return v.(*database.Relay)
	}
	return nil
}

// deployments is the deployments the caller can see: all of them for the
// admin, its own for a relay.
func (a *API) deployments(c *gin.Context) *gorm.DB {
	if r := relayOf(c); r != nil {
		return a.db.Where("relay_id = ?", r.ID)
	}
	return a.db
}

// ownRelay refuses a relay putting a deployment on another relay; the
// admin can put one anywhere.
func ownRelay(c *gin.Context, in *deploymentInput) bool {
	if r := relayOf(c); r != nil && in.RelayID != nil && *in.RelayID != r.ID {
		abort(c, http.StatusForbidden, "a relay can only manage deployments on itself")
		return false
	}
	return true
}

// relayMe tells a relay who it is and whether it may manage deployments;
// it works without that permission, so the CLI can say why it can't.
func (a *API) relayMe(c *gin.Context) {
	r := relayOf(c)
	c.JSON(http.StatusOK, a.relayOut(*r, a.hub.State()))
}

func (a *API) relayRoutes(v1 *gin.RouterGroup) {
	self := v1.Group("/relay", a.requireRelay)
	self.GET("/me", a.relayMe)

	m := self.Group("", a.requireManage)
	m.GET("/domains", a.listDomains)
	m.GET("/deployments", a.listDeployments)
	m.POST("/deployments", a.createDeployment)
	m.GET("/deployments/:id", a.getDeployment)
	m.PATCH("/deployments/:id", a.updateDeployment)
	m.DELETE("/deployments/:id", a.deleteDeployment)
	m.POST("/deployments/:id/tokens", a.createToken)
	m.DELETE("/deployments/:id/tokens/:tid", a.deleteToken)
}
