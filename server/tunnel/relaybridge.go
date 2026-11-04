package tunnel

import (
	"context"
	"net"
	"slices"
)

// The relay's side of the bridges that listen on its machine.

type activeListener struct {
	l  Listener
	bl *BridgeListener
}

func (r *Relay) features() []string {
	if r.Features != nil {
		return r.Features
	}
	return Features
}

func (r *Relay) setLink(l link) {
	r.curMu.Lock()
	r.cur = l
	r.curMu.Unlock()
}

// unsetLink forgets l unless a newer session has taken its place.
func (r *Relay) unsetLink(l link) {
	r.curMu.Lock()
	if r.cur == l {
		r.cur = nil
	}
	r.curMu.Unlock()
}

func (r *Relay) currentLink() link {
	r.curMu.Lock()
	defer r.curMu.Unlock()
	return r.cur
}

// openBridge opens a stream for bridge id on the current session. It
// follows the relay when it moves to a new session.
func (r *Relay) openBridge(id string) Opener {
	return func(ctx context.Context) (net.Conn, error) {
		l := r.currentLink()
		if l == nil {
			return nil, ErrOffline
		}
		st, err := l.Open(ctx)
		if err != nil {
			return nil, ErrOffline
		}
		if err := exchange(ctx, st, StreamHeader{Bridge: id}); err != nil {
			return nil, err
		}
		return st, nil
	}
}

// setListeners makes the relay listen on exactly ls: new ones start,
// removed or changed ones stop. A port that can't be bound is retried
// with every health round and reported in the health map meanwhile.
func (r *Relay) setListeners(ls []Listener) {
	r.lmu.Lock()
	r.wantListen = slices.Clone(ls)
	r.lmu.Unlock()
	r.retryListeners()
}

func (r *Relay) retryListeners() {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	if r.listeners == nil {
		r.listeners = map[string]*activeListener{}
	}
	want := make(map[string]Listener, len(r.wantListen))
	for _, l := range r.wantListen {
		want[l.ID] = l
	}
	for id, a := range r.listeners {
		if w, ok := want[id]; !ok || w != a.l {
			a.bl.Close()
			delete(r.listeners, id)
		}
	}
	health := make(map[string]string, len(want))
	for id, l := range want {
		if _, ok := r.listeners[id]; ok {
			health[id] = "ok"
			continue
		}
		bl, err := ListenBridge(l, r.openBridge(id), nil)
		if err != nil {
			if r.listenHealth[id] != "listen: "+err.Error() {
				r.Logf("relay: bridge %s on %s/%d: %v", id, l.Protocol, l.Port, err)
			}
			health[id] = "listen: " + err.Error()
			continue
		}
		r.listeners[id] = &activeListener{l: l, bl: bl}
		health[id] = "ok"
		r.Logf("relay: bridge %s listening on %s %s:%d", id, l.Protocol, l.Host, l.Port)
	}
	r.listenHealth = health
}

func (r *Relay) listenerHealth() map[string]string {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	out := make(map[string]string, len(r.listenHealth))
	for k, v := range r.listenHealth {
		out[k] = v
	}
	return out
}

// OpenBridge opens a stream for bridge id through the server, as the
// relay's own listener would: raw bytes for a tcp bridge, frames for udp.
func (r *Relay) OpenBridge(ctx context.Context, id string) (net.Conn, error) {
	return r.openBridge(id)(ctx)
}
