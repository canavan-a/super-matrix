// Package edge is the server's front door: it routes by Host header between
// the admin host (API + web app) and deployments, enforces each deployment's
// auth, and reverse-proxies through the deployment's relay.
package edge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"main/config"
	"main/database"
	"main/guard"
	"main/registry"
	"main/tunnel"
)

// Upstream connections are dialed to "<deployment id>.<session>.deployment:80",
// so the transport pools idle connections per deployment and relay session:
// when a relay reconnects (or moves to QUIC), new requests use the new
// session and the old one's idle connections just time out.
const dialSuffix = ".deployment"

type ctxKey struct{}

type Edge struct {
	cfg config.Config
	reg *registry.Registry
	hub *tunnel.Hub
	// Admin serves the admin host: the API and the web app.
	Admin http.Handler
	// TokenUsed is told when a deployment token gets a request through.
	TokenUsed func(tokenID string)

	key        []byte
	now        func() time.Time
	transport  *http.Transport
	proxy      *httputil.ReverseProxy
	loginLimit *guard.Limiter
	failLimit  *guard.Limiter
	basic      basicCache
}

func New(cfg config.Config, reg *registry.Registry, hub *tunnel.Hub, cookieKey []byte, admin http.Handler) *Edge {
	e := &Edge{
		cfg: cfg, reg: reg, hub: hub, key: cookieKey, Admin: admin, now: time.Now,
		loginLimit: guard.NewLimiter(10, time.Minute),
		failLimit:  guard.NewLimiter(10, time.Minute),
		basic:      basicCache{ok: map[string]time.Time{}},
	}
	e.transport = &http.Transport{
		DialContext:         e.dial,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		// Upstream is plain HTTP/1.1 over the tunnel stream; the relay does
		// any TLS to the target itself.
		ForceAttemptHTTP2: false,
	}
	e.proxy = &httputil.ReverseProxy{
		Rewrite:      e.rewrite,
		Transport:    e.transport,
		ErrorHandler: e.proxyError,
	}
	return e
}

// Changed drops pooled upstream connections, e.g. after a deployment moved
// to another relay or target.
func (e *Edge) Changed() { e.transport.CloseIdleConnections() }

func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := guard.StripPort(r.Host)
	if host == e.cfg.AdminHost {
		e.Admin.ServeHTTP(w, r)
		return
	}
	ent := e.reg.ByHost(host)
	if ent == nil || !ent.Enabled {
		message(w, http.StatusNotFound, "Nothing here", "There's no deployment at "+host+".")
		return
	}
	ip := guard.ClientIP(r, e.cfg.TrustProxy)
	if !ent.Allowed(ip) {
		message(w, http.StatusForbidden, "Not allowed", "Your address ("+ip+") isn't on this deployment's allow list.")
		return
	}
	if ent.AuthMode == database.AuthLogin && strings.HasPrefix(r.URL.Path, "/_supermatrix/") {
		e.loginRoutes(w, r, ent, ip)
		return
	}
	if !e.authorize(w, r, ent, ip) {
		return
	}
	ctx := context.WithValue(r.Context(), ctxKey{}, ent)
	ctx = context.WithValue(ctx, clientIPKey{}, ip)
	e.proxy.ServeHTTP(w, r.WithContext(ctx))
}

type clientIPKey struct{}

func (e *Edge) rewrite(pr *httputil.ProxyRequest) {
	ent := pr.In.Context().Value(ctxKey{}).(*registry.Entry)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = ent.ID + "." + strconv.FormatUint(e.hub.Gen(ent.RelayID), 10) + dialSuffix
	if ent.PreserveHost {
		pr.Out.Host = pr.In.Host
	} else {
		pr.Out.Host = targetHost(ent)
	}
	pr.SetXForwarded()
	pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
	if ip, _ := pr.In.Context().Value(clientIPKey{}).(string); ip != "" {
		pr.Out.Header.Set("X-Forwarded-For", ip)
		pr.Out.Header.Set("X-Real-IP", ip)
	}
	if e.cfg.TLSOn() {
		pr.Out.Header.Set("X-Forwarded-Proto", "https")
	}
}

// targetHost is the Host header the target expects when the public one
// isn't kept: host[:port], leaving out the protocol's default port.
func targetHost(ent *registry.Entry) string {
	if (ent.Protocol == "https" && ent.TargetPort == 443) || (ent.Protocol != "https" && ent.TargetPort == 80) {
		return ent.TargetHost
	}
	return net.JoinHostPort(ent.TargetHost, strconv.Itoa(ent.TargetPort))
}

func (e *Edge) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	key, ok := strings.CutSuffix(host, dialSuffix)
	id, _, _ := strings.Cut(key, ".")
	if !ok {
		return nil, errors.New("edge: bad upstream address")
	}
	ent := e.reg.ByID(id)
	if ent == nil || !ent.Enabled {
		return nil, errors.New("edge: deployment gone")
	}
	return e.hub.Dial(ctx, ent.RelayID, id)
}

func (e *Edge) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return // client went away
	}
	var te *tunnel.TargetError
	switch {
	case errors.Is(err, tunnel.ErrOffline):
		message(w, http.StatusBadGateway, "Relay offline", "The relay serving this deployment isn't connected right now.")
	case errors.As(err, &te):
		message(w, http.StatusBadGateway, "Can't reach the service", "The relay is up but couldn't connect to the service behind it: "+te.Msg)
	default:
		message(w, http.StatusBadGateway, "Bad gateway", "The service didn't answer properly.")
	}
}
