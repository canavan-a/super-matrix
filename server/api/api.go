// Package api is the admin surface on the admin host: root login, relays,
// deployments and their tokens, live status over a WebSocket, and the
// endpoint relays connect to.
package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/config"
	"main/database"
	"main/guard"
	"main/registry"
	"main/static"
	"main/tunnel"
	"main/version"
)

// Certs is what the API needs from the cert manager (nil with TLS off).
type Certs interface{ Warm(host string) }

type API struct {
	db  *gorm.DB
	cfg config.Config
	reg *registry.Registry
	hub *tunnel.Hub
	now func() time.Time

	// Certs warms certificates for new hosts; nil when TLS is off.
	Certs Certs
	// AfterChange runs after every deployment/relay change (edge pool reset).
	AfterChange func()

	// Lookup resolves a hostname (the domains DNS check); tests replace it.
	Lookup func(ctx context.Context, host string) ([]string, error)

	// quic is where relays can reach the QUIC listener; nil when it's off.
	quic atomic.Pointer[tunnel.QUICEndpoint]

	loginLimit *guard.Limiter
	watchers   *watchers

	tokenMu   sync.Mutex
	tokenSeen map[string]time.Time
}

func New(db *gorm.DB, cfg config.Config, reg *registry.Registry, hub *tunnel.Hub) *API {
	a := &API{
		db: db, cfg: cfg, reg: reg, hub: hub, now: time.Now,
		Lookup:     net.DefaultResolver.LookupHost,
		loginLimit: guard.NewLimiter(10, time.Minute),
		watchers:   newWatchers(),
		tokenSeen:  map[string]time.Time{},
	}
	hub.OnChange = func(string) { a.broadcastStatus() }
	hub.OnHello = a.onHello
	go a.statusTicker()
	return a
}

// SetClock replaces the API's clock (tests).
func (a *API) SetClock(now func() time.Time) { a.now = now }

// Handler serves the admin host: /health and /v1/* from the API, everything
// else from the web app.
func (a *API) Handler() http.Handler {
	router := a.Router()
	var web http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Super Matrix server " + version.Version + " is running.\nThe web app isn't installed here (SUPERMATRIX_STATIC_DIR).\n"))
	})
	if a.cfg.StaticDir != "" {
		web = static.Handler(a.cfg.StaticDir)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/v1/") {
			router.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
}

func (a *API) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// guard.ClientIP reads forwarding headers itself.
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery())

	// Any origin: the dev web client runs on another port, and every request
	// is authenticated by bearer token, not cookies.
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowAllOrigins = true
	corsConfig.AllowHeaders = []string{"Content-Type", "Authorization"}
	corsConfig.AllowMethods = []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"}
	r.Use(cors.New(corsConfig))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "app": "supermatrix", "version": version.Version})
	})

	v1 := r.Group("/v1")
	v1.POST("/login", a.login)
	v1.GET("/relay/connect", a.relayConnect)
	v1.GET("/relay/transports", a.relayTransports)
	v1.GET("/ws", a.ws)

	admin := v1.Group("", a.requireAdmin)
	admin.POST("/logout", a.logout)
	admin.GET("/info", a.info)
	admin.GET("/status", a.status)

	admin.GET("/relays", a.listRelays)
	admin.POST("/relays", a.createRelay)
	admin.PATCH("/relays/:id", a.updateRelay)
	admin.DELETE("/relays/:id", a.deleteRelay)
	admin.POST("/relays/:id/rotate", a.rotateRelay)

	admin.GET("/domains", a.listDomains)
	admin.POST("/domains", a.createDomain)
	admin.DELETE("/domains/:id", a.deleteDomain)
	admin.GET("/domains/:id/dns", a.domainDNS)

	admin.GET("/deployments", a.listDeployments)
	admin.POST("/deployments", a.createDeployment)
	admin.GET("/deployments/:id", a.getDeployment)
	admin.PATCH("/deployments/:id", a.updateDeployment)
	admin.DELETE("/deployments/:id", a.deleteDeployment)
	admin.POST("/deployments/:id/tokens", a.createToken)
	admin.DELETE("/deployments/:id/tokens/:tid", a.deleteToken)

	return r
}

func abort(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

func (a *API) clientIP(c *gin.Context) string { return guard.ClientIP(c.Request, a.cfg.TrustProxy) }

// publicURL is how a browser reaches host: https in production; in dev
// (TLS off) plain http on whatever port the admin request came in on.
func (a *API) publicURL(c *gin.Context, host string) string {
	if a.cfg.TLSOn() {
		return "https://" + host
	}
	if i := strings.LastIndex(c.Request.Host, ":"); i >= 0 && !strings.HasSuffix(c.Request.Host, "]") {
		return "http://" + host + c.Request.Host[i:]
	}
	return "http://" + host
}

func (a *API) info(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"adminHost": a.cfg.AdminHost,
		"tls":       a.cfg.TLSOn(),
		"version":   version.Version,
		"serverUrl": a.publicURL(c, a.cfg.AdminHost),
		"quic":      a.quic.Load(),
	})
}

// changed brings everything derived from the database up to date after a
// relay, deployment or token changes.
func (a *API) changed() {
	if err := a.reg.Reload(); err != nil {
		return
	}
	for _, id := range a.hub.Online() {
		a.hub.Push(id, a.reg.Targets(id))
	}
	if a.Certs != nil {
		var pending []string
		a.db.Model(&database.Deployment{}).Where("enabled = ? AND cert_status = ?", true, database.CertPending).Pluck("host", &pending)
		for _, host := range pending {
			a.Certs.Warm(host)
		}
	}
	if a.AfterChange != nil {
		a.AfterChange()
	}
	a.watchers.send(gin.H{"type": "changed"})
}

// CertStatus records a warm-up's result for the deployment at host.
func (a *API) CertStatus(host string, expires time.Time, err error) {
	upd := map[string]any{"cert_status": database.CertOK, "cert_error": "", "cert_expires_at": expires}
	if err != nil {
		upd = map[string]any{"cert_status": database.CertError, "cert_error": err.Error()}
	}
	a.db.Model(&database.Deployment{}).Where("host = ?", host).Updates(upd)
	_ = a.reg.Reload()
	a.watchers.send(gin.H{"type": "changed"})
}

// TokenUsed stamps a deployment token's last use, at most once a minute.
func (a *API) TokenUsed(id string) {
	now := a.now()
	a.tokenMu.Lock()
	last, ok := a.tokenSeen[id]
	if ok && now.Sub(last) < time.Minute {
		a.tokenMu.Unlock()
		return
	}
	a.tokenSeen[id] = now
	a.tokenMu.Unlock()
	go a.db.Model(&database.DeploymentToken{}).Where("id = ?", id).Update("last_used_at", now)
}

// SetQUIC sets where relays are told to reach QUIC (nil: off).
func (a *API) SetQUIC(ep *tunnel.QUICEndpoint) { a.quic.Store(ep) }
