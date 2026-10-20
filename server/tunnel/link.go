package tunnel

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// Transports a relay session can run over.
const (
	TransportQUIC      = "quic"
	TransportWebSocket = "websocket"
)

// link is one relay session's multiplexed connection: QUIC streams, or
// yamux over a WebSocket. Either end can open streams on it.
type link interface {
	Open(ctx context.Context) (net.Conn, error)
	Accept(ctx context.Context) (net.Conn, error)
	Close() error
	Done() <-chan struct{}
	Stats() LinkStats
	Transport() string
}

// LinkStats is what a link knows about the path it runs over.
type LinkStats struct {
	// RTT is the smoothed round-trip time (QUIC's own estimate, or yamux
	// pings for a WebSocket).
	RTT           time.Duration
	BytesSent     uint64
	BytesReceived uint64
	// PacketsLost is QUIC only; TCP hides its retransmissions.
	PacketsLost uint64
}

// --- QUIC ---

type quicLink struct{ c *quic.Conn }

func (l *quicLink) Open(ctx context.Context) (net.Conn, error) {
	s, err := l.c.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStream{Stream: s, c: l.c}, nil
}

func (l *quicLink) Accept(ctx context.Context) (net.Conn, error) {
	s, err := l.c.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStream{Stream: s, c: l.c}, nil
}

func (l *quicLink) Close() error          { return l.c.CloseWithError(0, "") }
func (l *quicLink) Done() <-chan struct{} { return l.c.Context().Done() }
func (l *quicLink) Transport() string     { return TransportQUIC }
func (l *quicLink) Stats() LinkStats {
	s := l.c.ConnectionStats()
	return LinkStats{RTT: s.SmoothedRTT, BytesSent: s.BytesSent, BytesReceived: s.BytesReceived, PacketsLost: s.PacketsLost}
}

// quicStream is a QUIC stream as a net.Conn. Close ends both directions,
// like closing a TCP socket: the send side finishes (after what's already
// written), and the peer is told to stop sending.
type quicStream struct {
	*quic.Stream
	c *quic.Conn
}

func (s *quicStream) Close() error {
	s.CancelRead(0)
	return s.Stream.Close()
}

func (s *quicStream) LocalAddr() net.Addr  { return s.c.LocalAddr() }
func (s *quicStream) RemoteAddr() net.Addr { return s.c.RemoteAddr() }

// --- yamux over a WebSocket ---

type yamuxLink struct {
	s    *yamux.Session
	conn *countingConn
	rtt  atomic.Int64
}

func newYamuxLink(s *yamux.Session, conn *countingConn) *yamuxLink {
	l := &yamuxLink{s: s, conn: conn}
	go l.pinger()
	return l
}

// pinger keeps an RTT sample current, the way QUIC does on its own.
func (l *yamuxLink) pinger() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		if d, err := l.s.Ping(); err == nil {
			l.rtt.Store(int64(d))
		}
		select {
		case <-l.s.CloseChan():
			return
		case <-t.C:
		}
	}
}

func (l *yamuxLink) Open(context.Context) (net.Conn, error) { return l.s.OpenStream() }
func (l *yamuxLink) Accept(ctx context.Context) (net.Conn, error) {
	return l.s.AcceptStreamWithContext(ctx)
}
func (l *yamuxLink) Close() error          { return l.s.Close() }
func (l *yamuxLink) Done() <-chan struct{} { return l.s.CloseChan() }
func (l *yamuxLink) Transport() string     { return TransportWebSocket }
func (l *yamuxLink) Stats() LinkStats {
	return LinkStats{RTT: time.Duration(l.rtt.Load()), BytesSent: l.conn.out.Load(), BytesReceived: l.conn.in.Load()}
}

// countingConn counts the bytes a WebSocket carries for LinkStats.
type countingConn struct {
	net.Conn
	in, out atomic.Uint64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.in.Add(uint64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.out.Add(uint64(n))
	return n, err
}
