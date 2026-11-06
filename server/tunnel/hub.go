package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
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
	// OnBridge decides whether relay fromRelay may open a stream for a
	// bridge, and if so where it leads: the relay that reaches its target
	// and its protocol. Without it, relays can't open streams.
	OnBridge func(fromRelay, bridgeID string) (toRelay, protocol string, err error)
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
	// targets and listeners are the full sets last pushed; the relay is
	// sent the part its features allow (see filtered).
	targets   []Target
	listeners []Listener
	// pushMu keeps pushes in order, so an older set never lands last.
	pushMu sync.Mutex
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
	// Features is what the relay said it supports (e.g. "bridge-tcp").
	Features []string `json:"features"`
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

// Serve runs a relay's session until it ends, first pushing it targets
// and bridge listeners. A relay that connects again replaces its old
// session, which drains: it keeps the connections it has, but gets no new
// ones.
func (h *Hub) Serve(relayID, remote string, in *Incoming, targets []Target, listeners []Listener) error {
	defer in.link.Close()
	s := &session{relayID: relayID, remote: remote, connectedAt: time.Now(), link: in.link, ctrl: in.ctrl,
		gen: h.gen.Add(1), health: map[string]string{}, targets: targets, listeners: listeners}
	if in.quic {
		if err := s.send(AuthReply{}); err != nil {
			return err
		}
	}
	// Until the relay's hello says what it supports, it gets no bridges.
	if err := s.push(); err != nil {
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

	// Streams the relay opens are for its bridge listeners. A draining
	// session keeps taking them until it closes.
	go func() {
		for {
			st, err := s.link.Accept(context.Background())
			if err != nil {
				return
			}
			go h.bridge(s, st)
		}
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
				before := s.hello.Features
				s.hello = *m.Hello
				s.stateMu.Unlock()
				if !slices.Equal(before, m.Hello.Features) {
					// It can do more (or less) than assumed: resend.
					if err := s.push(); err != nil {
						return err
					}
				}
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

// bridge serves a stream a relay opened for one of its bridge listeners:
// if the relay may use that bridge, it's joined to a stream to the relay
// at the far end.
func (h *Hub) bridge(s *session, st net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	var hdr StreamHeader
	if err := readLine(st, &hdr); err != nil {
		st.Close()
		return
	}
	refuse := func(msg string) {
		_ = writeLine(st, StreamReply{Error: msg})
		st.Close()
	}
	if hdr.Bridge == "" || h.OnBridge == nil {
		refuse("relays can only open bridge streams")
		return
	}
	to, proto, err := h.OnBridge(s.relayID, hdr.Bridge)
	if err != nil {
		refuse(err.Error())
		return
	}
	if !s.has(BridgeFeature(proto)) {
		refuse("this relay didn't say it supports " + proto + " bridges")
		return
	}
	up, err := h.Dial(ctx, to, hdr.Bridge)
	if err != nil {
		refuse(err.Error())
		return
	}
	if err := writeLine(st, StreamReply{}); err != nil {
		st.Close()
		up.Close()
		return
	}
	_ = st.SetDeadline(time.Time{})
	s.active.Add(1)
	pipeHalf(&trackedConn{Conn: st, s: s}, up)
}

// has reports whether the relay's hello listed feature f.
func (s *session) has(f string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.hello.HasFeature(f)
}

// filtered is the part of the pushed targets and listeners the relay can
// handle: bridges only for protocols its hello listed.
func (s *session) filtered() ([]Target, []Listener) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	ts := make([]Target, 0, len(s.targets))
	for _, t := range s.targets {
		if (t.Protocol == "tcp" || t.Protocol == "udp") && !s.hello.HasFeature(BridgeFeature(t.Protocol)) {
			continue
		}
		ts = append(ts, t)
	}
	var ls []Listener
	for _, l := range s.listeners {
		if s.hello.HasFeature(BridgeFeature(l.Protocol)) {
			ls = append(ls, l)
		}
	}
	return ts, ls
}

// push sends the relay what it should be serving. Listeners only go to
// relays that know about bridges.
func (s *session) push() error {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	ts, ls := s.filtered()
	if err := s.send(Control{Type: "targets", Targets: ts}); err != nil {
		return err
	}
	if !s.has(FeatureBridgeTCP) && !s.has(FeatureBridgeUDP) {
		return nil
	}
	return s.send(Control{Type: "listeners", Listeners: nonNilL(ls)})
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

// Push sends a relay its current targets and bridge listeners, if it's
// online.
func (h *Hub) Push(relayID string, targets []Target, listeners []Listener) {
	if s := h.get(relayID); s != nil {
		s.stateMu.Lock()
		s.targets, s.listeners = targets, listeners
		s.stateMu.Unlock()
		if err := s.push(); err != nil {
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
			Features: append([]string{}, s.hello.Features...),
		}
		if rs.Transport == TransportWebSocket {
			rs.Fallback = s.hello.Fallback
		}
		s.stateMu.Unlock()
		out[s.relayID] = rs
	}
	return out
}

// Dial opens a connection to a deployment's (or bridge's) target through
// its relay.
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
	if err := exchange(ctx, st, StreamHeader{Deployment: deploymentID}); err != nil {
		return nil, err
	}
	s.active.Add(1)
	return &trackedConn{Conn: st, s: s}, nil
}

// exchange writes a stream's header and reads the reply, closing the
// stream on failure. A refusal from the other end is a *TargetError.
func exchange(ctx context.Context, st net.Conn, hdr StreamHeader) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	} else {
		_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	if err := writeLine(st, hdr); err != nil {
		st.Close()
		return err
	}
	var reply StreamReply
	if err := readLine(st, &reply); err != nil {
		st.Close()
		return fmt.Errorf("relay: %w", err)
	}
	if reply.Error != "" {
		st.Close()
		return &TargetError{Msg: reply.Error}
	}
	_ = st.SetDeadline(time.Time{})
	return nil
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

func nonNilL(l []Listener) []Listener {
	if l == nil {
		return []Listener{}
	}
	return l
}

// CloseWrite half-closes the stream underneath (see pipeHalf).
func (c *trackedConn) CloseWrite() error { return closeWrite(c.Conn) }
