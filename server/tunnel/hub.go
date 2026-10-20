package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOffline = errors.New("relay offline")

// drainFor is how long a replaced session may keep serving the
// connections it already has (e.g. after a relay moves from the WebSocket
// to QUIC) before it's closed anyway.
var drainFor = 10 * time.Minute

// Hub is the server's side of every connected relay.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*session
	gen      atomic.Uint64
	// OnChange is called (on its own goroutine) when a relay connects,
	// disconnects, says hello or reports health.
	OnChange func(relayID string)
	// OnHello is called when a relay identifies itself.
	OnHello func(relayID string, h Hello, remote string)
}

type session struct {
	relayID     string
	remote      string
	connectedAt time.Time
	link        link
	// gen is unique per session; the edge keys its connection pool on it,
	// so a new session never reuses an old session's idle connections.
	gen    uint64
	active atomic.Int64

	ctrlMu sync.Mutex
	ctrl   net.Conn

	stateMu sync.Mutex
	hello   Hello
	health  map[string]string
}

// RelayState is a connected relay's live state.
type RelayState struct {
	Online      bool              `json:"online"`
	ConnectedAt time.Time         `json:"connectedAt"`
	Remote      string            `json:"remote"`
	Version     string            `json:"version"`
	Hostname    string            `json:"hostname"`
	Health      map[string]string `json:"health"`
	// Transport is "quic" or "websocket"; Fallback is the relay's reason
	// for not using QUIC, when it's on the WebSocket.
	Transport string `json:"transport"`
	Fallback  string `json:"fallback,omitempty"`
	// RTTMs is the link's smoothed round-trip time in milliseconds (0 until
	// measured).
	RTTMs         float64 `json:"rttMs"`
	BytesSent     uint64  `json:"bytesSent"`
	BytesReceived uint64  `json:"bytesReceived"`
	PacketsLost   uint64  `json:"packetsLost"`
	// Streams is how many proxied connections are open right now.
	Streams int64 `json:"streams"`
}

func NewHub() *Hub { return &Hub{sessions: map[string]*session{}} }

// Incoming is a relay connection that has arrived but isn't being served
// yet: over the WebSocket it's already authenticated (by its header); over
// QUIC the caller checks Token, then calls Hub.Serve or Reject.
type Incoming struct {
	// Token is the relay token a QUIC relay sent; empty for a WebSocket.
	Token string
	// Remote is the relay's address as the server sees it.
	Remote string

	link link
	ctrl net.Conn
	quic bool
}

// Transport is "quic" or "websocket".
func (in *Incoming) Transport() string { return in.link.Transport() }

// Reject refuses a QUIC relay (bad token) and closes its connection.
func (in *Incoming) Reject(msg string) {
	_ = in.ctrl.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = writeLine(in.ctrl, AuthReply{Error: msg})
	if ql, ok := in.link.(*quicLink); ok {
		_ = ql.c.CloseWithError(errUnauthorized, msg)
		return
	}
	in.link.Close()
}

// Serve runs a relay's session until it ends, first pushing it targets. A
// relay that connects again replaces its old session, which drains: it
// keeps the connections it has, but gets no new ones.
func (h *Hub) Serve(relayID, remote string, in *Incoming, targets []Target) error {
	defer in.link.Close()
	s := &session{relayID: relayID, remote: remote, connectedAt: time.Now(), link: in.link, ctrl: in.ctrl,
		gen: h.gen.Add(1), health: map[string]string{}}
	if in.quic {
		if err := s.send(AuthReply{}); err != nil {
			return err
		}
	}
	if err := s.send(Control{Type: "targets", Targets: nonNil(targets)}); err != nil {
		return err
	}

	h.mu.Lock()
	old := h.sessions[relayID]
	h.sessions[relayID] = s
	h.mu.Unlock()
	if old != nil {
		go old.drain()
	}
	h.changed(relayID)

	defer func() {
		h.mu.Lock()
		if h.sessions[relayID] == s {
			delete(h.sessions, relayID)
		}
		h.mu.Unlock()
		h.changed(relayID)
	}()

	lr := newLineReader(in.ctrl)
	for {
		var m Control
		if err := lr.next(&m); err != nil {
			return err
		}
		if h.get(relayID) != s {
			continue // draining; its reports are stale
		}
		switch m.Type {
		case "hello":
			if m.Hello != nil {
				s.stateMu.Lock()
				s.hello = *m.Hello
				s.stateMu.Unlock()
				if h.OnHello != nil {
					go h.OnHello(relayID, *m.Hello, remote)
				}
				h.changed(relayID)
			}
		case "health":
			s.stateMu.Lock()
			s.health = m.Health
			s.stateMu.Unlock()
			h.changed(relayID)
		}
	}
}

// drain closes a replaced session once its last proxied connection ends
// (idle pooled connections time out on their own), or after drainFor.
func (s *session) drain() {
	deadline := time.After(drainFor)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for s.active.Load() > 0 {
		select {
		case <-s.link.Done():
			return
		case <-deadline:
			s.link.Close()
			return
		case <-tick.C:
		}
	}
	s.link.Close()
}

func (h *Hub) changed(relayID string) {
	if h.OnChange != nil {
		go h.OnChange(relayID)
	}
}

func (s *session) send(m any) error {
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	_ = s.ctrl.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return writeLine(s.ctrl, m)
}

func (h *Hub) get(relayID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[relayID]
}

// Push sends a relay its current targets, if it's online.
func (h *Hub) Push(relayID string, targets []Target) {
	if s := h.get(relayID); s != nil {
		if err := s.send(Control{Type: "targets", Targets: nonNil(targets)}); err != nil {
			s.link.Close()
		}
	}
}

// Online lists the connected relays.
func (h *Hub) Online() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// Disconnect drops a relay's session (deleted, or its token rotated).
func (h *Hub) Disconnect(relayID string) {
	if s := h.get(relayID); s != nil {
		s.link.Close()
	}
}

// Gen identifies a relay's current session (0 when it's offline).
func (h *Hub) Gen(relayID string) uint64 {
	if s := h.get(relayID); s != nil {
		return s.gen
	}
	return 0
}

// State is a snapshot of every connected relay.
func (h *Hub) State() map[string]RelayState {
	h.mu.Lock()
	ss := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		ss = append(ss, s)
	}
	h.mu.Unlock()
	out := make(map[string]RelayState, len(ss))
	for _, s := range ss {
		st := s.link.Stats()
		s.stateMu.Lock()
		health := make(map[string]string, len(s.health))
		for k, v := range s.health {
			health[k] = v
		}
		rs := RelayState{
			Online: true, ConnectedAt: s.connectedAt, Remote: s.remote,
			Version: s.hello.Version, Hostname: s.hello.Hostname, Health: health,
			Transport: s.link.Transport(),
			RTTMs:     float64(st.RTT.Microseconds()) / 1000, BytesSent: st.BytesSent, BytesReceived: st.BytesReceived,
			PacketsLost: st.PacketsLost, Streams: s.active.Load(),
		}
		if rs.Transport == TransportWebSocket {
			rs.Fallback = s.hello.Fallback
		}
		s.stateMu.Unlock()
		out[s.relayID] = rs
	}
	return out
}

// Dial opens a connection to a deployment's target through its relay.
func (h *Hub) Dial(ctx context.Context, relayID, deploymentID string) (net.Conn, error) {
	s := h.get(relayID)
	if s == nil {
		return nil, ErrOffline
	}
	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := s.link.Open(openCtx)
	if err != nil {
		return nil, ErrOffline
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	} else {
		_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	if err := writeLine(st, StreamHeader{Deployment: deploymentID}); err != nil {
		st.Close()
		return nil, err
	}
	var reply StreamReply
	if err := readLine(st, &reply); err != nil {
		st.Close()
		return nil, fmt.Errorf("relay: %w", err)
	}
	if reply.Error != "" {
		st.Close()
		return nil, &TargetError{Msg: reply.Error}
	}
	_ = st.SetDeadline(time.Time{})
	s.active.Add(1)
	return &trackedConn{Conn: st, s: s}, nil
}

// trackedConn counts a session's open proxied connections.
type trackedConn struct {
	net.Conn
	s    *session
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.s.active.Add(-1) })
	return c.Conn.Close()
}

// TargetError is the relay's report that it couldn't reach a target.
type TargetError struct{ Msg string }

func (e *TargetError) Error() string { return "target unreachable: " + e.Msg }

func nonNil(t []Target) []Target {
	if t == nil {
		return []Target{}
	}
	return t
}
