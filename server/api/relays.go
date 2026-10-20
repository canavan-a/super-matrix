package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"main/database"
	"main/tunnel"
)

type relayDTO struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastSeenAt  *time.Time `json:"lastSeenAt"`
	LastIP      string     `json:"lastIp"`
	Version     string     `json:"version"`
	Hostname    string     `json:"hostname"`
	Online      bool       `json:"online"`
	Transport   string     `json:"transport"`
	Fallback    string     `json:"fallback"`
	Deployments int64      `json:"deployments"`
}

func (a *API) relayOut(r database.Relay, online map[string]tunnel.RelayState) relayDTO {
	var n int64
	a.db.Model(&database.Deployment{}).Where("relay_id = ?", r.ID).Count(&n)
	_, on := online[r.ID]
	return relayDTO{
		ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, LastIP: r.LastIP,
		Version: r.Version, Hostname: r.Hostname, Online: on, Deployments: n,
		Transport: r.Transport, Fallback: r.Fallback,
	}
}

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func (a *API) findRelay(c *gin.Context) *database.Relay {
	r, err := database.First[database.Relay](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if r == nil {
		abort(c, http.StatusNotFound, "no such relay")
		return nil
	}
	return r
}

func (a *API) listRelays(c *gin.Context) {
	var rs []database.Relay
	if err := a.db.Order("name").Find(&rs).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	online := a.hub.State()
	out := make([]relayDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, a.relayOut(r, online))
	}
	c.JSON(http.StatusOK, out)
}

// createRelay makes a relay and returns its token, the only time it's shown.
func (a *API) createRelay(c *gin.Context) {
	var b struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&b); err != nil || cleanName(b.Name) == "" {
		abort(c, http.StatusBadRequest, "name required")
		return
	}
	tok := database.NewSecret("smr")
	r := database.Relay{ID: database.NewID(), Name: cleanName(b.Name), TokenHash: database.HashSecret(tok), CreatedAt: a.now()}
	if err := a.db.Create(&r).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, gin.H{"relay": a.relayOut(r, a.hub.State()), "token": tok})
}

func (a *API) updateRelay(c *gin.Context) {
	r := a.findRelay(c)
	if r == nil {
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&b); err != nil || cleanName(b.Name) == "" {
		abort(c, http.StatusBadRequest, "name required")
		return
	}
	r.Name = cleanName(b.Name)
	a.db.Model(r).Update("name", r.Name)
	a.changed()
	c.JSON(http.StatusOK, a.relayOut(*r, a.hub.State()))
}

func (a *API) deleteRelay(c *gin.Context) {
	r := a.findRelay(c)
	if r == nil {
		return
	}
	var n int64
	a.db.Model(&database.Deployment{}).Where("relay_id = ?", r.ID).Count(&n)
	if n > 0 {
		abort(c, http.StatusConflict, "move or delete this relay's deployments first")
		return
	}
	a.db.Delete(r)
	a.hub.Disconnect(r.ID)
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// rotateRelay replaces a relay's token and drops its current connection.
func (a *API) rotateRelay(c *gin.Context) {
	r := a.findRelay(c)
	if r == nil {
		return
	}
	tok := database.NewSecret("smr")
	a.db.Model(r).Update("token_hash", database.HashSecret(tok))
	a.hub.Disconnect(r.ID)
	a.changed()
	c.JSON(http.StatusOK, gin.H{"token": tok})
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// Relays and the app authenticate with tokens, not cookies.
	CheckOrigin: func(*http.Request) bool { return true },
}

// relayByToken finds the relay a bearer token belongs to (nil if none).
func (a *API) relayByToken(tok string) (*database.Relay, error) {
	if tok == "" {
		return nil, nil
	}
	return database.First[database.Relay](a.db.Where("token_hash = ?", database.HashSecret(tok)))
}

// relayTransports tells a relay how it can connect; it asks before trying
// QUIC, so the server decides whether (and where) QUIC is on.
func (a *API) relayTransports(c *gin.Context) {
	r, err := a.relayByToken(bearer(c.Request))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if r == nil {
		abort(c, http.StatusUnauthorized, "unknown relay token")
		return
	}
	c.JSON(http.StatusOK, tunnel.Transports{QUIC: a.quic.Load()})
}

// relayConnect is where relays dial in over the WebSocket; the session
// lives until either end drops it.
func (a *API) relayConnect(c *gin.Context) {
	r, err := a.relayByToken(bearer(c.Request))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	if r == nil {
		abort(c, http.StatusUnauthorized, "unknown relay token")
		return
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	in, err := tunnel.FromWebSocket(ws)
	if err != nil {
		return
	}
	a.serveRelay(r, a.clientIP(c), in)
}

// ServeQUIC accepts relays on the QUIC listener until it's closed.
func (a *API) ServeQUIC(ln *tunnel.QUICListener) error {
	for {
		in, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			r, err := a.relayByToken(in.Token)
			if err != nil || r == nil {
				in.Reject("unknown relay token")
				return
			}
			a.serveRelay(r, in.Remote, in)
		}()
	}
}

func (a *API) serveRelay(r *database.Relay, ip string, in *tunnel.Incoming) {
	seen := func(fields map[string]any) {
		fields["last_seen_at"] = a.now()
		fields["last_ip"] = ip
		a.db.Model(&database.Relay{}).Where("id = ?", r.ID).Updates(fields)
	}
	seen(map[string]any{"transport": in.Transport()})
	log.Printf("relay %q connected from %s over %s", r.Name, ip, in.Transport())
	err := a.hub.Serve(r.ID, ip, in, a.reg.Targets(r.ID))
	log.Printf("relay %q (%s) disconnected: %v", r.Name, in.Transport(), err)
	seen(map[string]any{})
	a.watchers.send(gin.H{"type": "changed"})
}

func (a *API) onHello(relayID string, h tunnel.Hello, _ string) {
	a.db.Model(&database.Relay{}).Where("id = ?", relayID).Updates(map[string]any{
		"version": cleanName(h.Version), "hostname": cleanName(h.Hostname), "fallback": clip(h.Fallback, 300),
	})
	a.watchers.send(gin.H{"type": "changed"})
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
