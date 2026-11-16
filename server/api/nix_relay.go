package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
)

// Relays and their own Nix services. With "Manage its Nix services" on
// (Relay.ManageNixServices, off by default, like "Manage its
// deployments"), a relay's token can define Nix services, apply them to
// itself, and run, change and remove them there. What it defines is its
// own (owner_relay_id): only it and the admin see it, and it runs only on
// that relay; the admin can adopt one to share it. A relay never sees or
// touches the admin's services. They run as root on its machine, which is
// why it's a permission of its own.
//
// These routes are only reachable with the relay token itself, never
// through the relay's unauthenticated local API (which only forwards
// /me, /domains and /deployments), so on the machine it takes root.

// nixScope is the Nix services the caller can see: all of them for the
// admin, its own for a relay.
func (a *API) nixScope(c *gin.Context) *gorm.DB {
	if r := relayOf(c); r != nil {
		return a.db.Where("owner_relay_id = ?", r.ID)
	}
	return a.db
}

// appliedScope is the applied services the caller can see: all for the
// admin; for a relay, its own services on itself.
func (a *API) appliedScope(c *gin.Context) *gorm.DB {
	if r := relayOf(c); r != nil {
		return a.db.Where("relay_id = ? AND nix_service_id IN (SELECT id FROM nix_services WHERE owner_relay_id = ?)", r.ID, r.ID)
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

// ownedElsewhere refuses putting a relay's own service on another relay.
func ownedElsewhere(s *database.NixService, relayID string) error {
	if s.OwnerRelayID != nil && *s.OwnerRelayID != relayID {
		return conflictError(s.Slug + " was defined by a relay and runs only there: adopt it first to run it elsewhere")
	}
	return nil
}

// adoptNixService makes a relay's service the admin's: the relay can no
// longer change it, and it can run on any relay.
func (a *API) adoptNixService(c *gin.Context) {
	s := a.findNixService(c)
	if s == nil {
		return
	}
	if s.OwnerRelayID == nil {
		abort(c, http.StatusConflict, "it's already yours")
		return
	}
	a.db.Model(s).Updates(map[string]any{"owner_relay_id": nil, "updated_at": a.now()})
	s.OwnerRelayID = nil
	a.nixChanged()
	c.JSON(http.StatusOK, a.nixServiceFull(*s))
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
	c.JSON(http.StatusOK, a.nixServiceFull(*s))
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
