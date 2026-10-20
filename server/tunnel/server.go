package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// errUnauthorized is the QUIC application error code for a refused token.
const errUnauthorized quic.ApplicationErrorCode = 0x101

// FromWebSocket starts a session on an authenticated relay WebSocket. The
// server is the yamux client and opens the control stream.
func FromWebSocket(ws *websocket.Conn) (*Incoming, error) {
	cc := &countingConn{Conn: NewWSConn(ws)}
	mux, err := yamux.Client(cc, muxConfig())
	if err != nil {
		ws.Close()
		return nil, err
	}
	ctrl, err := mux.OpenStream()
	if err != nil {
		mux.Close()
		return nil, err
	}
	return &Incoming{link: newYamuxLink(mux, cc), ctrl: ctrl}, nil
}

// QUICListener accepts relays on a UDP port.
type QUICListener struct {
	ln       *quic.Listener
	incoming chan *Incoming
	done     chan struct{}
	once     sync.Once
}

// ListenQUIC listens on addr (UDP). tlsConf supplies the certificate; its
// NextProtos is replaced with ALPN.
func ListenQUIC(addr string, tlsConf *tls.Config) (*QUICListener, error) {
	t := tlsConf.Clone()
	t.NextProtos = []string{ALPN}
	t.MinVersion = tls.VersionTLS13
	ln, err := quic.ListenAddr(addr, t, quicConfig(4))
	if err != nil {
		return nil, err
	}
	l := &QUICListener{ln: ln, incoming: make(chan *Incoming), done: make(chan struct{})}
	go l.run()
	return l, nil
}

func (l *QUICListener) Addr() net.Addr { return l.ln.Addr() }
func (l *QUICListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.ln.Close()
}

// Accept returns the next relay that has sent its token.
func (l *QUICListener) Accept() (*Incoming, error) {
	select {
	case in := <-l.incoming:
		return in, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *QUICListener) run() {
	for {
		c, err := l.ln.Accept(context.Background())
		if err != nil {
			return
		}
		go func() {
			in, err := quicHandshake(c)
			if err != nil {
				_ = c.CloseWithError(0x100, "handshake failed")
				return
			}
			select {
			case l.incoming <- in:
			case <-l.done:
				in.link.Close()
			}
		}()
	}
}

// quicHandshake reads the relay's token off the first stream it opens.
func quicHandshake(c *quic.Conn) (*Incoming, error) {
	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
	defer cancel()
	st, err := c.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	ctrl := &quicStream{Stream: st, c: c}
	_ = ctrl.SetReadDeadline(time.Now().Add(10 * time.Second))
	var auth QUICAuth
	if err := readLine(ctrl, &auth); err != nil {
		return nil, err
	}
	if auth.Token == "" {
		return nil, errors.New("no token")
	}
	_ = ctrl.SetReadDeadline(time.Time{})
	remote := c.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	return &Incoming{Token: auth.Token, Remote: remote, link: &quicLink{c: c}, ctrl: ctrl, quic: true}, nil
}
