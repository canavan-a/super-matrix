// Package registry keeps an in-memory snapshot of every deployment, built
// from SQLite, so the edge never touches the database on the request path.
// Anything that changes deployments, their tokens, bridges or IP groups
// calls Reload.
package registry

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"main/config"
	"main/database"
	"main/guard"
	"main/tunnel"
)

// Entry is a deployment as the edge needs it.
type Entry struct {
	database.Deployment
	Host string
	// Nets is the parsed IP allowlist, with its IP groups' entries folded
	// in; empty allows everyone unless Restricted.
	Nets []netip.Prefix
	// Restricted is set when the allow list includes an IP group, so Nets
	// limits who gets in even if it ends up empty.
	Restricted bool
	// Tokens maps token hashes to their tokens.
	Tokens map[string]Token
	// PathRules are this deployment's path-prefix allowlists, longest
	// PathPrefix first, so the first match is the most specific one.
	PathRules []PathRuleEntry
	// OTPEmails is the set of addresses that can get a sign-in code;
	// "*@domain" stands for everyone at that domain.
	OTPEmails map[string]bool
	// rateLimiter and burstLimiter are nil unless RateLimitEnabled; a
	// request must pass both (RateLimitAllow) to proceed.
	rateLimiter  *guard.Limiter
	burstLimiter *guard.Limiter
}

// RateLimitAllow reports whether ip may make another request to e right
// now, per its configured per-client-IP rate limit. Always true when rate
// limiting is off for this deployment. When it isn't allowed, retryAfter is
// how long until the limit that rejected it resets.
func (e *Entry) RateLimitAllow(ip string, now time.Time) (ok bool, retryAfter time.Duration) {
	if !e.RateLimitEnabled {
		return true, 0
	}
	key := guard.LimitKey(ip)
	ok = true
	if e.rateLimiter != nil && !e.rateLimiter.Allow(key, now) {
		ok = false
		retryAfter = e.rateLimiter.RetryAfter(key, now)
	}
	if e.burstLimiter != nil && !e.burstLimiter.Allow(key, now) {
		ok = false
		if d := e.burstLimiter.RetryAfter(key, now); d > retryAfter {
			retryAfter = d
		}
	}
	return ok, retryAfter
}

// AllowsEmail reports whether a normalized address is on the email list,
// by its own entry or a *@domain one. Subdomains aren't covered.
func (e *Entry) AllowsEmail(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || email[:at] == "*" {
		return false
	}
	return e.OTPEmails[email] || e.OTPEmails["*"+email[at:]]
}

// PathRuleEntry is a path rule as the edge needs it.
type PathRuleEntry struct {
	ID         string
	PathPrefix string
	// Nets is the parsed extra IP allowlist; empty adds no restriction.
	Nets []netip.Prefix
}

// Token is a deployment API token as the edge needs it.
type Token struct {
	ID string
	// ExpiresAt is zero for a token that never expires.
	ExpiresAt time.Time
}

type snapshot struct {
	byHost  map[string]*Entry
	byID    map[string]*Entry
	byRelay map[string][]tunnel.Target
	// listens is the bridge listeners each relay runs.
	listens map[string][]tunnel.Listener
	bridges map[string]*BridgeEntry
}

// BridgeEntry is a bridge as the server needs it.
type BridgeEntry struct {
	database.Bridge
	// Nets is the parsed IP allowlist (public bridges), with its IP groups'
	// entries folded in; empty allows everyone unless Restricted.
	Nets []netip.Prefix
	// Restricted is set when the allow list includes an IP group.
	Restricted bool
}

// Allowed reports whether ip may use a public bridge.
func (b *BridgeEntry) Allowed(ip netip.Addr) bool {
	if len(b.Nets) == 0 && !b.Restricted {
		return true
	}
	ip = ip.Unmap()
	for _, p := range b.Nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Listener is the bridge's near end as a relay (or the server) runs it.
func (b *BridgeEntry) Listener() tunnel.Listener {
	return tunnel.Listener{ID: b.ID, Protocol: b.Protocol, Host: b.ListenHost, Port: b.ListenPort, IdleS: b.UDPIdleS}
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
	var bridges []database.Bridge
	if err := r.db.Find(&bridges).Error; err != nil {
		return err
	}
	var toks []database.DeploymentToken
	if err := r.db.Select("id", "deployment_id", "token_hash", "expires_at").Find(&toks).Error; err != nil {
		return err
	}
	var groups []database.IPGroup
	if err := r.db.Find(&groups).Error; err != nil {
		return err
	}
	groupNets := make(map[string][]netip.Prefix, len(groups))
	for _, g := range groups {
		groupNets[g.ID], _ = ParseAllowlist(g.Entries)
	}
	var depGroups []database.DeploymentIPGroup
	if err := r.db.Find(&depGroups).Error; err != nil {
		return err
	}
	var bridgeGroups []database.BridgeIPGroup
	if err := r.db.Find(&bridgeGroups).Error; err != nil {
		return err
	}
	var rules []database.PathRule
	if err := r.db.Find(&rules).Error; err != nil {
		return err
	}
	s := &snapshot{
		byHost:  make(map[string]*Entry, len(deps)),
		byID:    make(map[string]*Entry, len(deps)),
		byRelay: map[string][]tunnel.Target{},
		listens: map[string][]tunnel.Listener{},
		bridges: make(map[string]*BridgeEntry, len(bridges)),
	}
	for _, d := range deps {
		if d.Host == nil {
			continue // from before domains, with no SUPERMATRIX_BASE_DOMAIN to place it
		}
		e := &Entry{Deployment: d, Host: *d.Host, Tokens: map[string]Token{}, OTPEmails: map[string]bool{}}
		e.Nets, _ = ParseAllowlist(d.IPAllowlist)
		if d.RateLimitEnabled {
			e.rateLimiter = guard.NewLimiter(d.RateLimitRequests, time.Duration(d.RateLimitPeriodS)*time.Second)
			if d.RateLimitBurst > 0 {
				e.burstLimiter = guard.NewLimiter(d.RateLimitBurst, time.Second)
			}
		}
		for _, m := range strings.Split(d.OTPEmails, "\n") {
			if m = strings.TrimSpace(m); m != "" {
				e.OTPEmails[m] = true
			}
		}
		s.byHost[e.Host] = e
		s.byID[d.ID] = e
		if d.Enabled {
			s.byRelay[d.RelayID] = append(s.byRelay[d.RelayID], tunnel.Target{
				ID: d.ID, Protocol: d.Protocol, Host: d.TargetHost, Port: d.TargetPort,
				TLSSkipVerify: d.TLSSkipVerify, SNI: d.SNI,
			})
		}
	}
	for _, g := range depGroups {
		if e := s.byID[g.DeploymentID]; e != nil {
			e.Nets = append(e.Nets, groupNets[g.IPGroupID]...)
			e.Restricted = true
		}
	}
	for _, t := range toks {
		if e := s.byID[t.DeploymentID]; e != nil {
			tok := Token{ID: t.ID}
			if t.ExpiresAt != nil {
				tok.ExpiresAt = *t.ExpiresAt
			}
			e.Tokens[t.TokenHash] = tok
		}
	}
	for _, pr := range rules {
		if e := s.byID[pr.DeploymentID]; e != nil {
			nets, _ := ParseAllowlist(pr.IPAllowlist)
			e.PathRules = append(e.PathRules, PathRuleEntry{ID: pr.ID, PathPrefix: pr.PathPrefix, Nets: nets})
		}
	}
	for _, e := range s.byID {
		sort.Slice(e.PathRules, func(i, j int) bool {
			return len(e.PathRules[i].PathPrefix) > len(e.PathRules[j].PathPrefix)
		})
	}
	for _, b := range bridges {
		be := &BridgeEntry{Bridge: b}
		be.Nets, _ = ParseAllowlist(b.IPAllowlist)
		s.bridges[b.ID] = be
		if !b.Enabled {
			continue
		}
		s.byRelay[b.ToRelayID] = append(s.byRelay[b.ToRelayID], tunnel.Target{
			ID: b.ID, Protocol: b.Protocol, Host: b.TargetHost, Port: b.TargetPort, IdleS: b.UDPIdleS,
		})
		if b.FromRelayID != nil {
			s.listens[*b.FromRelayID] = append(s.listens[*b.FromRelayID], be.Listener())
		}
	}
	for _, g := range bridgeGroups {
		if be := s.bridges[g.BridgeID]; be != nil {
			be.Nets = append(be.Nets, groupNets[g.IPGroupID]...)
			be.Restricted = true
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

// Listeners is the bridges a relay should listen for.
func (r *Registry) Listeners(relayID string) []tunnel.Listener { return r.snap.Load().listens[relayID] }

// Bridge finds a bridge by ID, enabled or not.
func (r *Registry) Bridge(id string) *BridgeEntry { return r.snap.Load().bridges[id] }

// PublicBridges lists the enabled bridges that listen on the server.
func (r *Registry) PublicBridges() []*BridgeEntry {
	var out []*BridgeEntry
	for _, b := range r.snap.Load().bridges {
		if b.Enabled && b.Public() {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HostAllowed reports whether host is a deployment's hostname. Disabled
// ones count: they still answer, with the "disabled" page, over HTTPS.
func (r *Registry) HostAllowed(host string) bool { return r.ByHost(host) != nil }

// Hosts lists the hostnames of every deployment, disabled ones included.
func (r *Registry) Hosts() []string {
	var out []string
	for _, e := range r.snap.Load().byID {
		out = append(out, e.Host)
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
	if len(e.Nets) == 0 && !e.Restricted {
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

// PathAllowed reports whether ip may reach reqPath on e: the whole-host
// Allowed check plus, if reqPath falls under a configured path rule (its
// prefix equals reqPath, or reqPath has prefix+"/" as a literal prefix —
// so "/secret" matches "/secret" and "/secret/x" but not "/secretly"),
// that rule's own allowlist too. The most specific (longest) matching
// prefix wins; no match means no extra restriction.
func (e *Entry) PathAllowed(reqPath, ip string) bool {
	var rule *PathRuleEntry
	for i := range e.PathRules {
		p := e.PathRules[i].PathPrefix
		if reqPath == p || p == "/" || strings.HasPrefix(reqPath, p+"/") {
			rule = &e.PathRules[i]
			break
		}
	}
	if rule == nil || len(rule.Nets) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, n := range rule.Nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}
