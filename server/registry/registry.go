// Package registry keeps an in-memory snapshot of every deployment, built
// from SQLite, so the edge never touches the database on the request path.
// Anything that changes deployments or their tokens calls Reload.
package registry

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	"main/config"
	"main/database"
	"main/tunnel"
)

// Entry is a deployment as the edge needs it.
type Entry struct {
	database.Deployment
	Host string
	// Nets is the parsed IP allowlist; empty allows everyone.
	Nets []netip.Prefix
	// Tokens maps token hashes to token IDs.
	Tokens map[string]string
}

type snapshot struct {
	byHost  map[string]*Entry
	byID    map[string]*Entry
	byRelay map[string][]tunnel.Target
}

type Registry struct {
	db   *gorm.DB
	cfg  config.Config
	mu   sync.Mutex // serializes Reload
	snap atomic.Pointer[snapshot]
}

func New(db *gorm.DB, cfg config.Config) (*Registry, error) {
	r := &Registry{db: db, cfg: cfg}
	return r, r.Reload()
}

func (r *Registry) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var deps []database.Deployment
	if err := r.db.Find(&deps).Error; err != nil {
		return err
	}
	var toks []database.DeploymentToken
	if err := r.db.Select("id", "deployment_id", "token_hash").Find(&toks).Error; err != nil {
		return err
	}
	s := &snapshot{
		byHost:  make(map[string]*Entry, len(deps)),
		byID:    make(map[string]*Entry, len(deps)),
		byRelay: map[string][]tunnel.Target{},
	}
	for _, d := range deps {
		if d.Host == nil {
			continue // from before domains, with no SUPERMATRIX_BASE_DOMAIN to place it
		}
		e := &Entry{Deployment: d, Host: *d.Host, Tokens: map[string]string{}}
		e.Nets, _ = ParseAllowlist(d.IPAllowlist)
		s.byHost[e.Host] = e
		s.byID[d.ID] = e
		if d.Enabled {
			s.byRelay[d.RelayID] = append(s.byRelay[d.RelayID], tunnel.Target{
				ID: d.ID, Protocol: d.Protocol, Host: d.TargetHost, Port: d.TargetPort,
				TLSSkipVerify: d.TLSSkipVerify, SNI: d.SNI,
			})
		}
	}
	for _, t := range toks {
		if e := s.byID[t.DeploymentID]; e != nil {
			e.Tokens[t.TokenHash] = t.ID
		}
	}
	r.snap.Store(s)
	return nil
}

// ByHost finds a deployment by its hostname, enabled or not.
func (r *Registry) ByHost(host string) *Entry { return r.snap.Load().byHost[host] }

func (r *Registry) ByID(id string) *Entry { return r.snap.Load().byID[id] }

// Targets is what a relay should be forwarding.
func (r *Registry) Targets(relayID string) []tunnel.Target { return r.snap.Load().byRelay[relayID] }

// HostAllowed reports whether host is an enabled deployment's hostname.
func (r *Registry) HostAllowed(host string) bool {
	e := r.ByHost(host)
	return e != nil && e.Enabled
}

// Hosts lists the hostnames of every enabled deployment.
func (r *Registry) Hosts() []string {
	var out []string
	for _, e := range r.snap.Load().byID {
		if e.Enabled {
			out = append(out, e.Host)
		}
	}
	sort.Strings(out)
	return out
}

// ParseAllowlist parses newline/comma-separated CIDRs or bare addresses.
func ParseAllowlist(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\r' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Allowed reports whether ip may reach e.
func (e *Entry) Allowed(ip string) bool {
	if len(e.Nets) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range e.Nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
