package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Relays and Nix services. With "Manage its Nix services" on
// (Relay.ManageNixServices, off by default, like "Manage its
// deployments"), a relay's token can see and define Nix services, apply
// them to itself, and run, change and remove them there. Services are
// shared: one a relay defines is like the admin's, and can run on any
// relay. A relay only ever applies to itself, and only manages what's
// applied to it. They run as root on its machine, which is why it's a
// permission of its own.
//
// These routes are only reachable with the relay token itself, never
// through the relay's unauthenticated local API (which only forwards
// /me, /domains and /deployments), so on the machine it takes root.

// appliedScope is the applied services the caller can see: all for the
// admin; for a relay, the ones on itself.
func (a *API) appliedScope(c *gin.Context) *gorm.DB {
	if r := relayOf(c); r != nil {
		return a.db.Where("relay_id = ?", r.ID)
	}
	return a.db
}

// requireManageNix lets through relays allowed to manage their Nix
// services, and logs what they change (like requireManage for
// deployments).
func (a *API) requireManageNix(c *gin.Context) {
	r := relayOf(c)
	if !r.ManageNixServices {
		abort(c, http.StatusForbidden, "this relay isn't allowed to manage Nix services; turn on \"Manage its Nix services\" for "+r.Name+" in the app")
		return
	}
	c.Next()
	if c.Request.Method != http.MethodGet && c.Writer.Status() < 300 {
		log.Printf("relay %q: %s %s", r.Name, c.Request.Method, c.Request.URL.Path)
	}
}

// nixLockDecision accepts ({accept: true}) or turns down a flake.lock a
// relay made for the service. Accepted, every relay running it rebuilds
// with it.
func (a *API) nixLockDecision(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	var b struct {
		Accept bool `json:"accept"`
	}
	if err := c.ShouldBindJSON(&b); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if s.PendingLock == "" {
		abort(c, http.StatusConflict, "no flake.lock is waiting")
		return
	}
	upd := map[string]any{"pending_lock": "", "pending_lock_from": "", "updated_at": a.now()}
	if b.Accept {
		after := *s
		after.FlakeLock, after.LockPinned = s.PendingLock, false
		if !a.schemaAllowsUpdate(c, kindNixService, s.ID, s.Slug, s.Slug, nixServiceItem(*s, true), nixServiceItem(after, true)) {
			return
		}
		upd["flake_lock"] = s.PendingLock
		s.FlakeLock = s.PendingLock
	}
	a.db.Model(s).Updates(upd)
	s.PendingLock, s.PendingLockFrom = "", ""
	a.nixChanged()
	c.JSON(http.StatusOK, a.nixServiceFull(c, *s))
}

func (a *API) relayNixRoutes(self *gin.RouterGroup) {
	self = self.Group("", a.requireManageNix)
	self.GET("/nix-services", a.listNixServices)
	self.POST("/nix-services", a.createNixService)
	self.GET("/nix-services/:id", a.getNixService)
	self.PATCH("/nix-services/:id", a.updateNixService)
	self.DELETE("/nix-services/:id", a.deleteNixService)
	self.POST("/nix-services/:id/relock", a.relockNixService)
	self.POST("/nix-services/:id/applied", a.createApplied)
	self.GET("/applied-nix-services", a.listApplied)
	self.GET("/applied-nix-services/:aid", a.getApplied)
	self.PATCH("/applied-nix-services/:aid", a.updateApplied)
	self.DELETE("/applied-nix-services/:aid", a.deleteApplied)
	self.POST("/applied-nix-services/:aid/restart", a.appliedAction("restart"))
	self.POST("/applied-nix-services/:aid/rebuild", a.appliedAction("rebuild"))
	self.POST("/applied-nix-services/:aid/rollback", a.appliedAction("rollback"))
	self.GET("/applied-nix-services/:aid/events", a.appliedEvents)
	self.GET("/applied-nix-services/:aid/logs", a.appliedLogs)
}
