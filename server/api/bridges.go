package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"main/database"
	"main/registry"
	"main/tunnel"
)

type bridgeDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
	// Public: the bridge listens on the server, at PublicAddress.
	Public        bool     `json:"public"`
	PublicAddress string   `json:"publicAddress"`
	FromRelayID   string   `json:"fromRelayId"`
	ListenHost    string   `json:"listenHost"`
	ListenPort    int      `json:"listenPort"`
	ToRelayID     string   `json:"toRelayId"`
	TargetHost    string   `json:"targetHost"`
	TargetPort    int      `json:"targetPort"`
	UDPIdleS      int      `json:"udpIdleS"`
	IPAllowlist   []string `json:"ipAllowlist"`
	// DomainID and Subdomain give a public bridge the DNS name Host; ""
	// when it has none (its address is then the admin host's).
	DomainID  string `json:"domainId"`
	Subdomain string `json:"subdomain"`
	Host      string `json:"host"`
	// ListenError is why a public bridge's port couldn't be bound.
	ListenError string    `json:"listenError"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (a *API) bridgeOut(b database.Bridge) bridgeDTO {
	out := bridgeDTO{
		ID: b.ID, Name: b.Name, Protocol: b.Protocol, Enabled: b.Enabled, Public: b.Public(),
		ListenHost: b.ListenHost, ListenPort: b.ListenPort, ToRelayID: b.ToRelayID,
		TargetHost: b.TargetHost, TargetPort: b.TargetPort, UDPIdleS: b.UDPIdleS,
		IPAllowlist: splitAllowlist(b.IPAllowlist), CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
	if b.DomainID != nil {
		out.DomainID, out.Subdomain, out.Host = *b.DomainID, b.Subdomain, b.Host
	}
	if b.FromRelayID != nil {
		out.FromRelayID = *b.FromRelayID
	} else {
		host := a.cfg.AdminHost
		if b.Host != "" {
			host = b.Host
		}
		out.PublicAddress = net.JoinHostPort(host, strconv.Itoa(b.ListenPort))
		if a.Public != nil && b.Enabled {
			if h := a.Public.Health()[b.ID]; h != "ok" {
				out.ListenError = h
			}
		}
	}
	return out
}

func (a *API) findBridge(c *gin.Context) *database.Bridge {
	b, err := database.First[database.Bridge](a.db.Where("id = ?", c.Param("id")))
	if err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return nil
	}
	if b == nil {
		abort(c, http.StatusNotFound, "no such bridge")
		return nil
	}
	return b
}

func (a *API) listBridges(c *gin.Context) {
	var bs []database.Bridge
	if err := a.db.Order("name").Find(&bs).Error; err != nil {
		abort(c, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]bridgeDTO, 0, len(bs))
	for _, b := range bs {
		out = append(out, a.bridgeOut(b))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getBridge(c *gin.Context) {
	if b := a.findBridge(c); b != nil {
		c.JSON(http.StatusOK, a.bridgeOut(*b))
	}
}

// bridgeInput is the body of create (missing fields take defaults) and
// update (missing fields stay as they are).
type bridgeInput struct {
	Name     *string `json:"name"`
	Protocol *string `json:"protocol"`
	Enabled  *bool   `json:"enabled"`
	// Public makes the server the listening side; FromRelayID is then
	// ignored.
	Public      *bool     `json:"public"`
	FromRelayID *string   `json:"fromRelayId"`
	ListenHost  *string   `json:"listenHost"`
	ListenPort  *int      `json:"listenPort"`
	ToRelayID   *string   `json:"toRelayId"`
	TargetHost  *string   `json:"targetHost"`
	TargetPort  *int      `json:"targetPort"`
	UDPIdleS    *int      `json:"udpIdleS"`
	IPAllowlist *[]string `json:"ipAllowlist"`
	// DomainID (public bridges): a domain for its DNS name; "" for none.
	// Subdomain is the label under a wildcard domain.
	DomainID  *string `json:"domainId"`
	Subdomain *string `json:"subdomain"`
}

// conflictError is a 409: the bridge clashes with another, or a relay
// can't serve it.
type conflictError string

func (e conflictError) Error() string { return string(e) }

// bridgeKey is what, when it changes on an enabled bridge, makes the
// relays' support matter again.
func bridgeKey(b *database.Bridge) string {
	from := "public"
	if b.FromRelayID != nil {
		from = *b.FromRelayID
	}
	return fmt.Sprintf("%v|%s|%s|%s", b.Enabled, b.Protocol, from, b.ToRelayID)
}

// applyBridge copies the set fields of in onto b and checks the result.
func (a *API) applyBridge(b *database.Bridge, in bridgeInput, isNew bool) error {
	before := bridgeKey(b)
	if in.Name != nil {
		b.Name = cleanName(*in.Name)
	}
	if in.Protocol != nil {
		b.Protocol = strings.ToLower(strings.TrimSpace(*in.Protocol))
	}
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	}
	if in.Public != nil {
		if *in.Public {
			b.FromRelayID = nil
		} else if b.FromRelayID == nil && in.FromRelayID == nil {
			return inputError("a bridge that isn't public needs fromRelayId")
		}
	}
	if in.FromRelayID != nil && (in.Public == nil || !*in.Public) {
		id := *in.FromRelayID
		b.FromRelayID = &id
	}
	if in.ListenHost != nil {
		b.ListenHost = strings.TrimSpace(*in.ListenHost)
	}
	if in.ListenPort != nil {
		b.ListenPort = *in.ListenPort
	}
	if in.ToRelayID != nil {
		b.ToRelayID = *in.ToRelayID
	}
	if in.TargetHost != nil {
		b.TargetHost = strings.TrimSpace(*in.TargetHost)
	}
	if in.TargetPort != nil {
		b.TargetPort = *in.TargetPort
	}
	if in.UDPIdleS != nil {
		b.UDPIdleS = *in.UDPIdleS
	}
	if in.DomainID != nil {
		if *in.DomainID == "" {
			b.DomainID = nil
		} else {
			id := *in.DomainID
			b.DomainID = &id
		}
	}
	if in.Subdomain != nil {
		b.Subdomain = strings.ToLower(strings.TrimSpace(*in.Subdomain))
	}
	if in.IPAllowlist != nil {
		joined := strings.Join(*in.IPAllowlist, "\n")
		if _, err := registry.ParseAllowlist(joined); err != nil {
			return inputError("the IP allow list has an entry that isn't an address or CIDR")
		}
		var clean []string
		for _, f := range *in.IPAllowlist {
			if f = strings.TrimSpace(f); f != "" {
				clean = append(clean, f)
			}
		}
		b.IPAllowlist = strings.Join(clean, "\n")
	}

	switch {
	case b.Name == "":
		return inputError("name required")
	case b.Protocol != database.ProtocolTCP && b.Protocol != database.ProtocolUDP:
		return inputError(`protocol must be "tcp" or "udp"`)
	case b.ListenPort < 1 || b.ListenPort > 65535:
		return inputError("listen port must be 1-65535")
	case b.TargetPort < 1 || b.TargetPort > 65535:
		return inputError("target port must be 1-65535")
	case b.TargetHost == "" || !hostRe.MatchString(b.TargetHost):
		return inputError("target host required")
	case b.UDPIdleS < 30 || b.UDPIdleS > 86400:
		return inputError("UDP idle timeout must be 30 s to 24 h")
	}

	to, err := database.First[database.Relay](a.db.Where("id = ?", b.ToRelayID))
	if err != nil {
		return err
	}
	if to == nil {
		return inputError("no such relay (toRelayId)")
	}
	relays := []*database.Relay{to}

	if b.Public() {
		b.ListenHost = ""
		if err := a.bridgeHost(b); err != nil {
			return err
		}
		switch {
		case len(a.cfg.PublicPorts) == 0:
			return inputError("public bridges are off on this server (set SUPERMATRIX_PUBLIC_PORTS)")
		case !a.cfg.PublicPorts.Contains(b.ListenPort):
			return inputError("public bridges can use ports " + a.cfg.PublicPorts.String())
		case a.cfg.Reserved(b.Protocol, b.ListenPort):
			return inputError(fmt.Sprintf("%s port %d is the server's own", b.Protocol, b.ListenPort))
		}
		var n int64
		if err := a.db.Model(&database.Bridge{}).Where("from_relay_id IS NULL AND protocol = ? AND listen_port = ? AND id <> ?",
			b.Protocol, b.ListenPort, b.ID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return conflictError(fmt.Sprintf("another public bridge already uses %s port %d", b.Protocol, b.ListenPort))
		}
	} else {
		// A DNS name only means something for a port on the server.
		b.DomainID, b.Subdomain, b.Host = nil, "", ""
		if b.ListenHost == "" {
			b.ListenHost = "127.0.0.1"
		}
		if _, err := netip.ParseAddr(b.ListenHost); err != nil {
			return inputError("listen host must be an IP address, like 127.0.0.1")
		}
		if *b.FromRelayID == b.ToRelayID {
			return inputError("a bridge connects two different relays")
		}
		from, err := database.First[database.Relay](a.db.Where("id = ?", *b.FromRelayID))
		if err != nil {
			return err
		}
		if from == nil {
			return inputError("no such relay (fromRelayId)")
		}
		relays = append(relays, from)
		var n int64
		if err := a.db.Model(&database.Bridge{}).Where("from_relay_id = ? AND protocol = ? AND listen_host = ? AND listen_port = ? AND id <> ?",
			*b.FromRelayID, b.Protocol, b.ListenHost, b.ListenPort, b.ID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return conflictError(fmt.Sprintf("another bridge already listens on %s %s:%d on that relay", b.Protocol, b.ListenHost, b.ListenPort))
		}
	}

	// A relay that never said it can serve this kind of bridge would
	// silently ignore it, so don't let one be set up (or turned back on)
	// that way.
	if b.Enabled && (isNew || bridgeKey(b) != before) {
		feat := tunnel.BridgeFeature(b.Protocol)
		for _, r := range relays {
			if !r.HasFeature(feat) {
				return conflictError(fmt.Sprintf("relay %q doesn't support %s bridges yet: update it and let it connect", r.Name, strings.ToUpper(b.Protocol)))
			}
		}
	}
	return nil
}

func (a *API) bridgeFail(c *gin.Context, err error) {
	var ce conflictError
	if errors.As(err, &ce) {
		abort(c, http.StatusConflict, ce.Error())
		return
	}
	a.inputFail(c, err)
}

// bridgeWriteFail answers a failed insert or save. applyBridge already
// checked the port is free, but a concurrent request can take it between
// that check and the write; the unique index then catches it.
func bridgeWriteFail(c *gin.Context, b *database.Bridge, err error) {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if b.Public() {
			abort(c, http.StatusConflict, fmt.Sprintf("another public bridge already uses %s port %d", b.Protocol, b.ListenPort))
		} else {
			abort(c, http.StatusConflict, fmt.Sprintf("another bridge already listens on %s %s:%d on that relay", b.Protocol, b.ListenHost, b.ListenPort))
		}
		return
	}
	abort(c, http.StatusInternalServerError, "server error")
}

func (a *API) createBridge(c *gin.Context) {
	var in bridgeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	now := a.now()
	b := database.Bridge{ID: database.NewID(), Enabled: true, ListenHost: "127.0.0.1",
		UDPIdleS: int(tunnel.DefaultUDPIdle / time.Second), CreatedAt: now, UpdatedAt: now}
	if (in.Public == nil || !*in.Public) && in.FromRelayID == nil {
		abort(c, http.StatusBadRequest, "fromRelayId required (or public: true)")
		return
	}
	if in.TargetPort == nil && in.ListenPort != nil {
		in.TargetPort = in.ListenPort
	}
	if in.ListenPort == nil && in.TargetPort != nil {
		in.ListenPort = in.TargetPort
	}
	if err := a.applyBridge(&b, in, true); err != nil {
		a.bridgeFail(c, err)
		return
	}
	if err := a.db.Create(&b).Error; err != nil {
		bridgeWriteFail(c, &b, err)
		return
	}
	a.changed()
	c.JSON(http.StatusCreated, a.bridgeOut(b))
}

func (a *API) updateBridge(c *gin.Context) {
	b := a.findBridge(c)
	if b == nil {
		return
	}
	var in bridgeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		abort(c, http.StatusBadRequest, "bad json")
		return
	}
	if err := a.applyBridge(b, in, false); err != nil {
		a.bridgeFail(c, err)
		return
	}
	b.UpdatedAt = a.now()
	// Save with a nil FromRelayID writes NULL, which is what public means.
	if err := a.db.Select("*").Save(b).Error; err != nil {
		bridgeWriteFail(c, b, err)
		return
	}
	a.changed()
	c.JSON(http.StatusOK, a.bridgeOut(*b))
}

func (a *API) deleteBridge(c *gin.Context) {
	b := a.findBridge(c)
	if b == nil {
		return
	}
	a.db.Delete(b)
	a.changed()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// onBridge is the hub's check on a stream a relay opened: the bridge must
// exist, be on, and listen on that relay.
func (a *API) onBridge(fromRelay, id string) (string, string, error) {
	b := a.reg.Bridge(id)
	if b == nil || !b.Enabled || b.FromRelayID == nil || *b.FromRelayID != fromRelay {
		return "", "", errors.New("no such bridge on this relay")
	}
	return b.ToRelayID, b.Protocol, nil
}

// bridgeHost works out a public bridge's DNS name from its domain: the
// subdomain under a wildcard domain, or an exact domain itself. Several
// bridges (and a deployment) can share a name: they differ by port.
func (a *API) bridgeHost(b *database.Bridge) error {
	b.Host = ""
	if b.DomainID == nil {
		b.Subdomain = ""
		return nil
	}
	d, err := database.First[database.Domain](a.db.Where("id = ?", *b.DomainID))
	if err != nil {
		return err
	}
	if d == nil {
		return inputError("no such domain")
	}
	if !d.Wildcard {
		b.Subdomain = ""
	} else if !labelRe.MatchString(b.Subdomain) {
		return inputError("pick a name under " + d.Label() + ", like turn")
	}
	b.Host = d.HostFor(b.Subdomain)
	return nil
}

// bridgeDNS checks that a public bridge's DNS name points at this server.
func (a *API) bridgeDNS(c *gin.Context) {
	b := a.findBridge(c)
	if b == nil {
		return
	}
	if b.Host == "" {
		abort(c, http.StatusBadRequest, "this bridge has no DNS name")
		return
	}
	c.JSON(http.StatusOK, a.checkDNS(c, b.Host))
}
