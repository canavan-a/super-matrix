package tunnel

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// Relay is the runner side: it keeps a session to the server open and
// forwards the server's streams to local targets.
type Relay struct {
	// ServerURL is the server's admin origin, e.g. https://matrix.example.com.
	ServerURL string
	Token     string
	Version   string
	// Transport is "auto" (the default: QUIC, falling back to the
	// WebSocket), "quic" (QUIC only) or "websocket" (never QUIC).
	Transport string
	// QUICAddr overrides the host:port the server advertises for QUIC.
	QUICAddr string
	// QUICTimeout bounds a QUIC attempt before falling back (default 5s).
	QUICTimeout time.Duration
	// UpgradeEvery is how often a relay that fell back to the WebSocket
	// tries QUIC again (default 5m). A relay that gets through moves over
	// without dropping connections in flight.
	UpgradeEvery time.Duration
	// HealthEvery is how often targets are probed (default 30s).
	HealthEvery time.Duration
	Logf        func(format string, args ...any)

	mu      sync.Mutex
	targets map[string]Target

	stateMu   sync.Mutex
	transport string
	fallback  string
	quicErrAt time.Time
	quicErr   string
}

// ErrUnauthorized means the server rejected the token; retrying won't help
// until someone fixes the config, but Run keeps retrying slowly anyway.
var ErrUnauthorized = errors.New("server rejected the relay token")

// conn is an established session, before it's served.
type conn struct {
	link link
	ctrl net.Conn
	// fallback is why this WebSocket session isn't QUIC.
	fallback string
}

// Status is the relay's current transport ("" while disconnected) and, on
// the WebSocket, why it isn't using QUIC.
func (r *Relay) Status() (transport, fallback string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.transport, r.fallback
}

func (r *Relay) setStatus(transport, fallback string) {
	r.stateMu.Lock()
	r.transport, r.fallback = transport, fallback
	r.stateMu.Unlock()
}

func (r *Relay) mode() string {
	switch r.Transport {
	case TransportQUIC, TransportWebSocket:
		return r.Transport
	}
	return "auto"
}

// Run connects and reconnects (backing off 1s → 60s) until ctx is done.
func (r *Relay) Run(ctx context.Context) error {
	if r.Logf == nil {
		r.Logf = log.Printf
	}
	if r.HealthEvery == 0 {
		r.HealthEvery = 30 * time.Second
	}
	if r.QUICTimeout == 0 {
		r.QUICTimeout = 5 * time.Second
	}
	if r.UpgradeEvery == 0 {
		r.UpgradeEvery = 5 * time.Minute
	}
	backoff := time.Second
	var next *conn
	for {
		started := time.Now()
		c, err := next, error(nil)
		next = nil
		if c == nil {
			c, err = r.connect(ctx)
		}
		if err == nil {
			next, err = r.serve(ctx, c)
			if next != nil {
				continue
			}
		}
		r.setStatus("", "")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		r.Logf("relay: disconnected (%v); retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 60*time.Second)
	}
}

// connect opens a session: QUIC if the mode and the network allow it,
// otherwise the WebSocket, remembering why.
func (r *Relay) connect(ctx context.Context) (*conn, error) {
	mode := r.mode()
	if mode == TransportWebSocket {
		return r.dialWS(ctx, "transport set to websocket")
	}
	if mode == "auto" && r.proxied() {
		return r.dialWS(ctx, "an HTTP proxy is configured, which QUIC can't use")
	}
	if mode == "auto" {
		r.stateMu.Lock()
		recent := !r.quicErrAt.IsZero() && time.Since(r.quicErrAt) < r.UpgradeEvery
		why := r.quicErr
		r.stateMu.Unlock()
		if recent && why != "" {
			// It failed a moment ago; don't make every reconnect wait for
			// the QUIC timeout. The upgrade loop keeps trying.
			return r.dialWS(ctx, why)
		}
	}
	c, err := r.tryQUIC(ctx)
	if err == nil || mode == TransportQUIC || errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
		return c, err
	}
	return r.dialWS(ctx, err.Error())
}

// tryQUIC asks the server where its QUIC port is, then dials it. Errors
// read as fallback reasons.
func (r *Relay) tryQUIC(ctx context.Context) (*conn, error) {
	ep, err := r.discover(ctx)
	if err != nil {
		return nil, err
	}
	c, err := r.dialQUIC(ctx, ep)
	if err != nil && !errors.Is(err, ErrUnauthorized) {
		r.stateMu.Lock()
		r.quicErrAt = time.Now()
		r.quicErr = err.Error()
		r.stateMu.Unlock()
		r.Logf("relay: QUIC to %s failed: %v", ep.Addr, err)
	}
	return c, err
}

// proxied reports whether an HTTP(S) proxy is set for the server URL: then
// only the WebSocket (through the proxy) can get out.
func (r *Relay) proxied() bool {
	req, err := http.NewRequest(http.MethodGet, r.ServerURL, nil)
	if err != nil {
		return false
	}
	u, err := http.ProxyFromEnvironment(req)
	return err == nil && u != nil
}

// discover reads the server's QUIC endpoint (GET /v1/relay/transports).
func (r *Relay) discover(ctx context.Context) (*QUICEndpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.ServerURL, "/")+"/v1/relay/transports", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking the server for its QUIC port: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("asking the server for its QUIC port: %s", res.Status)
	}
	var t Transports
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&t); err != nil {
		return nil, fmt.Errorf("asking the server for its QUIC port: %w", err)
	}
	if t.QUIC == nil {
		return nil, errors.New("the server has QUIC turned off")
	}
	if r.QUICAddr != "" {
		t.QUIC.Addr = r.QUICAddr
	}
	return t.QUIC, nil
}

func (r *Relay) dialQUIC(ctx context.Context, ep *QUICEndpoint) (*conn, error) {
	tc := &tls.Config{ServerName: ep.ServerName, NextProtos: []string{ALPN}, MinVersion: tls.VersionTLS13}
	if ep.Pin != "" {
		// No public certificate (the server runs with TLS off): check the
		// self-signed one against the pin the server gave us instead.
		want := ep.Pin
		tc.InsecureSkipVerify = true //nolint:gosec // verified against the pin below
		tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || CertPin(raw[0]) != want {
				return errors.New("certificate doesn't match the server's pin")
			}
			return nil
		}
	}
	dctx, cancel := context.WithTimeout(ctx, r.QUICTimeout)
	defer cancel()
	qc, err := quic.DialAddr(dctx, ep.Addr, tc, quicConfig(1<<16))
	if err != nil {
		if dctx.Err() != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("no answer on udp %s within %s (UDP blocked?)", ep.Addr, r.QUICTimeout)
		}
		return nil, fmt.Errorf("quic: %w", err)
	}
	fail := func(err error) (*conn, error) {
		_ = qc.CloseWithError(0, "")
		var ae *quic.ApplicationError
		if errors.As(err, &ae) && ae.ErrorCode == errUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("quic: %w", err)
	}
	st, err := qc.OpenStreamSync(dctx)
	if err != nil {
		return fail(err)
	}
	ctrl := &quicStream{Stream: st, c: qc}
	_ = ctrl.SetDeadline(time.Now().Add(r.QUICTimeout))
	if err := writeLine(ctrl, QUICAuth{Token: r.Token}); err != nil {
		return fail(err)
	}
	var rep AuthReply
	if err := readLine(ctrl, &rep); err != nil {
		return fail(err)
	}
	if rep.Error != "" {
		_ = qc.CloseWithError(0, "")
		return nil, ErrUnauthorized
	}
	_ = ctrl.SetDeadline(time.Time{})
	return &conn{link: &quicLink{c: qc}, ctrl: ctrl}, nil
}

// CertPin is how a certificate is pinned: "sha256:<hex of the DER>".
func CertPin(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *Relay) connectURL() string {
	u := strings.TrimRight(r.ServerURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/v1/relay/connect"
}

func (r *Relay) dialWS(ctx context.Context, fallback string) (*conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	hdr := http.Header{"Authorization": {"Bearer " + r.Token}}
	ws, resp, err := dialer.DialContext(ctx, r.connectURL(), hdr)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	cc := &countingConn{Conn: NewWSConn(ws)}
	mux, err := yamux.Server(cc, muxConfig())
	if err != nil {
		ws.Close()
		return nil, err
	}
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctrl, err := mux.AcceptStreamWithContext(actx)
	if err != nil {
		mux.Close()
		return nil, err
	}
	return &conn{link: newYamuxLink(mux, cc), ctrl: ctrl, fallback: fallback}, nil
}

// serve runs a session until it ends. On the WebSocket in auto mode it
// keeps trying QUIC, and returns the QUIC session once it's up; the old one
// carries on with the connections it has until the server closes it.
func (r *Relay) serve(ctx context.Context, c *conn) (*conn, error) {
	host, _ := os.Hostname()
	var ctrlMu sync.Mutex
	send := func(m Control) error {
		ctrlMu.Lock()
		defer ctrlMu.Unlock()
		_ = c.ctrl.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return writeLine(c.ctrl, m)
	}
	hello := func(fallback string) error {
		return send(Control{Type: "hello", Hello: &Hello{Version: r.Version, Hostname: host, Fallback: fallback}})
	}
	if err := hello(c.fallback); err != nil {
		c.link.Close()
		return nil, err
	}
	tr := c.link.Transport()
	r.setStatus(tr, c.fallback)
	if tr == TransportQUIC {
		r.Logf("relay: connected to %s over QUIC", r.ServerURL)
	} else {
		r.Logf("relay: connected to %s over the WebSocket (not QUIC: %s)", r.ServerURL, c.fallback)
	}

	// Close the link when ctx ends, unless it's been handed over to drain.
	go func() {
		select {
		case <-ctx.Done():
			c.link.Close()
		case <-c.link.Done():
		}
	}()

	kick := make(chan struct{}, 1)
	done := make(chan struct{})
	defer close(done)
	go r.healthLoop(done, kick, send)
	go func() {
		lr := newLineReader(c.ctrl)
		for {
			var m Control
			if err := lr.next(&m); err != nil {
				c.link.Close()
				return
			}
			if m.Type == "targets" {
				r.setTargets(m.Targets)
				select {
				case kick <- struct{}{}:
				default:
				}
			}
		}
	}()

	acceptErr := make(chan error, 1)
	go func() {
		for {
			st, err := c.link.Accept(context.Background())
			if err != nil {
				acceptErr <- err
				return
			}
			go r.handle(st)
		}
	}()

	upgraded := make(chan *conn, 1)
	if tr == TransportWebSocket && r.mode() == "auto" && !r.proxied() {
		go r.upgradeLoop(done, upgraded, hello)
	}

	select {
	case err := <-acceptErr:
		return nil, err
	case n := <-upgraded:
		r.Logf("relay: QUIC works again; moving over from the WebSocket")
		go func() {
			select {
			case <-c.link.Done():
			case <-time.After(drainFor + time.Minute):
				c.link.Close()
			}
		}()
		return n, nil
	}
}

// upgradeLoop retries QUIC from a WebSocket session, updating the server
// on why it still can't.
func (r *Relay) upgradeLoop(done <-chan struct{}, out chan<- *conn, hello func(string) error) {
	t := time.NewTicker(r.UpgradeEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		c, err := r.tryQUIC(ctx)
		cancel()
		if err == nil {
			select {
			case out <- c:
			case <-done:
				c.link.Close()
			}
			return
		}
		if errors.Is(err, ErrUnauthorized) {
			continue
		}
		r.stateMu.Lock()
		changed := r.fallback != err.Error()
		if r.transport == TransportWebSocket {
			r.fallback = err.Error()
		}
		r.stateMu.Unlock()
		if changed {
			_ = hello(err.Error())
		}
	}
}

func (r *Relay) setTargets(ts []Target) {
	m := make(map[string]Target, len(ts))
	for _, t := range ts {
		m[t.ID] = t
	}
	r.mu.Lock()
	r.targets = m
	r.mu.Unlock()
	r.Logf("relay: serving %d deployment(s)", len(m))
}

func (r *Relay) target(id string) (Target, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.targets[id]
	return t, ok
}

func (r *Relay) handle(st net.Conn) {
	_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	var h StreamHeader
	if err := readLine(st, &h); err != nil {
		st.Close()
		return
	}
	t, ok := r.target(h.Deployment)
	if !ok {
		_ = writeLine(st, StreamReply{Error: "unknown deployment"})
		st.Close()
		return
	}
	conn, err := dialTarget(t, 10*time.Second)
	if err != nil {
		_ = writeLine(st, StreamReply{Error: err.Error()})
		st.Close()
		return
	}
	if err := writeLine(st, StreamReply{}); err != nil {
		st.Close()
		conn.Close()
		return
	}
	_ = st.SetDeadline(time.Time{})
	pipe(st, conn)
}

func dialTarget(t Target, timeout time.Duration) (net.Conn, error) {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if t.Protocol != "https" {
		return conn, nil
	}
	sni := t.SNI
	if sni == "" {
		sni = t.Host
	}
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: t.TLSSkipVerify}) //nolint:gosec // per-deployment opt-in for self-signed targets
	_ = tc.SetDeadline(time.Now().Add(timeout))
	if err := tc.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls: %w", err)
	}
	_ = tc.SetDeadline(time.Time{})
	return tc, nil
}

// pipe copies both ways until either side is done, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// healthLoop probes every target (a TCP connect, plus the TLS handshake for
// https targets) and reports the results whenever they change or are asked
// for.
func (r *Relay) healthLoop(done <-chan struct{}, kick <-chan struct{}, send func(Control) error) {
	tick := time.NewTicker(r.HealthEvery)
	defer tick.Stop()
	for {
		r.mu.Lock()
		ts := make([]Target, 0, len(r.targets))
		for _, t := range r.targets {
			ts = append(ts, t)
		}
		r.mu.Unlock()

		health := make(map[string]string, len(ts))
		var wg sync.WaitGroup
		var hmu sync.Mutex
		for _, t := range ts {
			wg.Add(1)
			go func(t Target) {
				defer wg.Done()
				status := "ok"
				if c, err := dialTarget(t, 5*time.Second); err != nil {
					status = err.Error()
				} else {
					c.Close()
				}
				hmu.Lock()
				health[t.ID] = status
				hmu.Unlock()
			}(t)
		}
		wg.Wait()
		if err := send(Control{Type: "health", Health: health}); err != nil {
			return
		}

		select {
		case <-done:
			return
		case <-kick:
		case <-tick.C:
		}
	}
}
