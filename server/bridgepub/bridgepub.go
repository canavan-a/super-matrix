// Package bridgepub runs public bridges: ports on the server itself that
// lead, through a relay, to a target on that relay's network. The server
// plays the part a relay plays for an ordinary bridge (tunnel.ListenBridge)
// and opens each stream straight to the far relay with Hub.Dial.
package bridgepub

import (
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"main/registry"
	"main/tunnel"
)

// Limits on who can hold what open on one public bridge.
const (
	perIPConns = 32   // TCP connections from one address
	perIPFlows = 64   // UDP senders (address:port) from one address
	perBridge  = 4096 // connections or senders in all
)

type active struct {
	l  tunnel.Listener
	bl *tunnel.BridgeListener

	mu    sync.Mutex
	perIP map[netip.Addr]int
	total int
}

// Manager keeps the server listening on exactly the enabled public
// bridges' ports.
type Manager struct {
	reg  *registry.Registry
	hub  *tunnel.Hub
	addr string

	mu     sync.Mutex
	active map[string]*active
	health map[string]string

	// OnChange is called when a bridge's listen status changes.
	OnChange func()
}

// New makes a manager that binds public bridges on addr ("" for all
// addresses).
func New(reg *registry.Registry, hub *tunnel.Hub, addr string) *Manager {
	return &Manager{reg: reg, hub: hub, addr: addr, active: map[string]*active{}, health: map[string]string{}}
}

// Run retries ports that couldn't be bound until ctx ends, then closes
// every listener.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-t.C:
			m.Reconcile()
		}
	}
}

// Reconcile opens listeners for new public bridges and closes those of
// removed, disabled or changed ones.
func (m *Manager) Reconcile() {
	want := map[string]tunnel.Listener{}
	for _, b := range m.reg.PublicBridges() {
		l := b.Listener()
		l.Host = m.addr
		want[b.ID] = l
	}
	m.mu.Lock()
	for id, a := range m.active {
		if w, ok := want[id]; !ok || w != a.l {
			a.bl.Close()
			delete(m.active, id)
		}
	}
	health := make(map[string]string, len(want))
	for id, l := range want {
		if _, ok := m.active[id]; ok {
			health[id] = "ok"
			continue
		}
		a := &active{l: l, perIP: map[netip.Addr]int{}}
		bl, err := tunnel.ListenBridge(l, m.opener(id), m.admit(id, a))
		if err != nil {
			if m.health[id] != "listen: "+err.Error() {
				log.Printf("public bridge %s on %s/%d: %v", id, l.Protocol, l.Port, err)
			}
			health[id] = "listen: " + err.Error()
			continue
		}
		a.bl = bl
		m.active[id] = a
		health[id] = "ok"
		log.Printf("public bridge %s listening on %s :%d", id, l.Protocol, l.Port)
	}
	changed := !sameMap(health, m.health)
	m.health = health
	m.mu.Unlock()
	if changed && m.OnChange != nil {
		m.OnChange()
	}
}

// Health is each public bridge's listen status: "ok" or the bind error.
func (m *Manager) Health() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.health))
	for k, v := range m.health {
		out[k] = v
	}
	return out
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.active {
		a.bl.Close()
		delete(m.active, id)
	}
}

var errGone = errors.New("bridge disabled or deleted")

func (m *Manager) opener(id string) tunnel.Opener {
	return func(ctx context.Context) (net.Conn, error) {
		b := m.reg.Bridge(id)
		if b == nil || !b.Enabled || !b.Public() {
			return nil, errGone
		}
		return m.hub.Dial(ctx, b.ToRelayID, id)
	}
}

// admit checks the bridge's allow list (as it is now, so edits apply to
// new connections without rebinding) and the per-address limits.
func (m *Manager) admit(id string, a *active) tunnel.Admit {
	limit := perIPConns
	if a.l.Protocol == "udp" {
		limit = perIPFlows
	}
	return func(ip netip.Addr) (func(), bool) {
		b := m.reg.Bridge(id)
		if b == nil || !b.Allowed(ip) {
			return nil, false
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.perIP[ip] >= limit || a.total >= perBridge {
			return nil, false
		}
		a.perIP[ip]++
		a.total++
		var once sync.Once
		return func() {
			once.Do(func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				if a.perIP[ip]--; a.perIP[ip] <= 0 {
					delete(a.perIP, ip)
				}
				a.total--
			})
		}, true
	}
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
