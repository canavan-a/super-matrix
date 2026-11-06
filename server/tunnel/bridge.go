package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

// A bridge is a TCP or UDP port on one side (a relay, or the server for a
// public bridge) that leads to a target on another relay's network. The
// listening side carries each TCP connection on its own stream, and all
// of a UDP port's packets on one stream of frames (see udpframe.go). The
// far side is the relay's usual target handling: TCP is a byte pipe, UDP
// gets serveUDPTarget.

// Opener opens a stream to a bridge's far end, past the header and reply,
// ready for raw bytes (tcp) or frames (udp).
type Opener func(ctx context.Context) (net.Conn, error)

// Admit decides whether a new TCP connection or UDP sender from addr may
// use a bridge. release is called once it's gone. A nil Admit lets
// everyone in.
type Admit func(addr netip.Addr) (release func(), ok bool)

// DefaultUDPIdle is how long a UDP sender may stay quiet before its flow
// is forgotten, when a bridge doesn't say.
const DefaultUDPIdle = 300 * time.Second

// maxUDPFlows caps the senders one UDP bridge keeps track of; past it,
// the one quiet the longest is dropped.
const maxUDPFlows = 1024

// openTimeout bounds opening a bridge stream through the server.
const openTimeout = 20 * time.Second

func idleOf(s int) time.Duration {
	if s <= 0 {
		return DefaultUDPIdle
	}
	return time.Duration(s) * time.Second
}

// BridgeListener is a bridge's bound port.
type BridgeListener struct {
	close func()
	addr  net.Addr
	udp   *udpBridge // nil for tcp
}

// Addr is the address it listens on.
func (b *BridgeListener) Addr() net.Addr { return b.addr }

// Close stops listening and ends the connections and flows it carries.
func (b *BridgeListener) Close() { b.close() }

// ListenBridge binds l's port and carries what arrives there over streams
// from open, until Close.
func ListenBridge(l Listener, open Opener, admit Admit) (*BridgeListener, error) {
	addr := net.JoinHostPort(l.Host, strconv.Itoa(l.Port))
	switch l.Protocol {
	case "tcp":
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		t := &tcpBridge{ln: ln, open: open, admit: admit, conns: map[net.Conn]struct{}{}}
		go t.serve()
		return &BridgeListener{close: t.shut, addr: ln.Addr()}, nil
	case "udp":
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		pc, err := net.ListenUDP("udp", ua)
		if err != nil {
			return nil, err
		}
		u := &udpBridge{pc: pc, open: open, admit: admit, idle: idleOf(l.IdleS),
			byAddr: map[netip.AddrPort]*udpFlow{}, byID: map[uint32]*udpFlow{}, done: make(chan struct{})}
		go u.serve()
		go u.sweep()
		return &BridgeListener{close: u.shut, addr: pc.LocalAddr(), udp: u}, nil
	}
	return nil, fmt.Errorf("unknown bridge protocol %q", l.Protocol)
}

func remoteAddr(a net.Addr) netip.Addr {
	switch a := a.(type) {
	case *net.TCPAddr:
		return a.AddrPort().Addr().Unmap()
	case *net.UDPAddr:
		return a.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

// --- TCP ---

type tcpBridge struct {
	ln    net.Listener
	open  Opener
	admit Admit

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func (t *tcpBridge) serve() {
	for {
		c, err := t.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond) // e.g. out of file descriptors
			continue
		}
		go t.handle(c)
	}
}

func (t *tcpBridge) handle(c net.Conn) {
	if t.admit != nil {
		release, ok := t.admit(remoteAddr(c.RemoteAddr()))
		if !ok {
			c.Close()
			return
		}
		defer release()
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		c.Close()
		return
	}
	t.conns[c] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.conns, c)
		t.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	st, err := t.open(ctx)
	cancel()
	if err != nil {
		c.Close()
		return
	}
	pipeHalf(c, st)
}

func (t *tcpBridge) shut() {
	t.mu.Lock()
	t.closed = true
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	t.ln.Close()
	for _, c := range conns {
		c.Close()
	}
}

// --- UDP, listening side ---

type udpFlow struct {
	id      uint32
	addr    netip.AddrPort
	last    atomic.Int64 // unix nanos of the last packet either way
	release func()
}

type udpBridge struct {
	pc    *net.UDPConn
	open  Opener
	admit Admit
	idle  time.Duration

	mu     sync.Mutex
	byAddr map[netip.AddrPort]*udpFlow
	byID   map[uint32]*udpFlow
	next   uint32

	// streamMu guards st, the stream every flow shares, opened on the first
	// packet and again after it fails.
	streamMu sync.Mutex
	st       net.Conn
	wmu      sync.Mutex

	done chan struct{}
	once sync.Once
}

func (u *udpBridge) serve() {
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := u.pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		f := u.flow(from)
		if f == nil {
			continue
		}
		f.last.Store(time.Now().UnixNano())
		u.send(f.id, buf[:n])
	}
}

// flow finds or makes the flow for a sender; nil if it isn't admitted.
func (u *udpBridge) flow(from netip.AddrPort) *udpFlow {
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	u.mu.Lock()
	defer u.mu.Unlock()
	if f := u.byAddr[from]; f != nil {
		return f
	}
	release := func() {}
	if u.admit != nil {
		r, ok := u.admit(from.Addr())
		if !ok {
			return nil
		}
		release = r
	}
	if len(u.byAddr) >= maxUDPFlows {
		var oldest *udpFlow
		for _, f := range u.byAddr {
			if oldest == nil || f.last.Load() < oldest.last.Load() {
				oldest = f
			}
		}
		u.forget(oldest)
	}
	u.next++
	f := &udpFlow{id: u.next, addr: from, release: release}
	u.byAddr[from] = f
	u.byID[f.id] = f
	return f
}

// forget drops a flow; u.mu is held.
func (u *udpBridge) forget(f *udpFlow) {
	delete(u.byAddr, f.addr)
	delete(u.byID, f.id)
	f.release()
}

// send carries one packet to the far side, opening the stream if needed
// and retrying once on a fresh stream if the old one has failed. A packet
// that can't be sent is dropped, as UDP would.
func (u *udpBridge) send(id uint32, p []byte) {
	for range 2 {
		st, err := u.stream()
		if err != nil {
			return
		}
		u.wmu.Lock()
		err = writeFrame(st, id, p)
		u.wmu.Unlock()
		if err == nil {
			return
		}
		u.dropStream(st)
	}
}

func (u *udpBridge) stream() (net.Conn, error) {
	u.streamMu.Lock()
	defer u.streamMu.Unlock()
	select {
	case <-u.done:
		return nil, net.ErrClosed
	default:
	}
	if u.st != nil {
		return u.st, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	st, err := u.open(ctx)
	if err != nil {
		return nil, err
	}
	u.st = st
	go u.replies(st)
	return st, nil
}

func (u *udpBridge) dropStream(st net.Conn) {
	u.streamMu.Lock()
	if u.st == st {
		u.st = nil
	}
	u.streamMu.Unlock()
	st.Close()
}

// replies hands packets from the far side to the senders they're for.
func (u *udpBridge) replies(st net.Conn) {
	defer u.dropStream(st)
	br := bufio.NewReaderSize(st, 64<<10)
	buf := make([]byte, maxDatagram)
	for {
		id, p, err := readFrame(br, buf)
		if err != nil {
			return
		}
		u.mu.Lock()
		f := u.byID[id]
		u.mu.Unlock()
		if f == nil {
			continue
		}
		f.last.Store(time.Now().UnixNano())
		_, _ = u.pc.WriteToUDPAddrPort(p, f.addr)
	}
}

func (u *udpBridge) sweep() {
	t := time.NewTicker(sweepEvery(u.idle))
	defer t.Stop()
	for {
		select {
		case <-u.done:
			return
		case <-t.C:
		}
		cutoff := time.Now().Add(-u.idle).UnixNano()
		u.mu.Lock()
		for _, f := range u.byAddr {
			if f.last.Load() < cutoff {
				u.forget(f)
			}
		}
		u.mu.Unlock()
	}
}

func sweepEvery(idle time.Duration) time.Duration {
	return min(max(idle/4, 50*time.Millisecond), 30*time.Second)
}

func (u *udpBridge) shut() {
	u.once.Do(func() {
		close(u.done)
		u.pc.Close()
		u.streamMu.Lock()
		if u.st != nil {
			u.st.Close()
			u.st = nil
		}
		u.streamMu.Unlock()
		u.mu.Lock()
		for _, f := range u.byAddr {
			u.forget(f)
		}
		u.mu.Unlock()
	})
}

// --- UDP, far side ---

type farFlow struct {
	c    *net.UDPConn
	last atomic.Int64
}

// serveUDPTarget is a relay's end of a UDP bridge: every flow on the
// stream gets its own socket to the target, so replies can be told apart
// and labelled with the flow they belong to. It returns when the stream
// ends.
func serveUDPTarget(st net.Conn, t Target) {
	idle := idleOf(t.IdleS)
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	var (
		mu    sync.Mutex
		wmu   sync.Mutex
		flows = map[uint32]*farFlow{}
		done  = make(chan struct{})
	)
	drop := func(id uint32, f *farFlow) { // mu held
		delete(flows, id)
		f.c.Close()
	}
	defer func() {
		close(done)
		st.Close()
		mu.Lock()
		for id, f := range flows {
			drop(id, f)
		}
		mu.Unlock()
	}()
	go func() {
		tk := time.NewTicker(sweepEvery(idle))
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
			}
			cutoff := time.Now().Add(-idle).UnixNano()
			mu.Lock()
			for id, f := range flows {
				if f.last.Load() < cutoff {
					drop(id, f)
				}
			}
			mu.Unlock()
		}
	}()

	br := bufio.NewReaderSize(st, 64<<10)
	buf := make([]byte, maxDatagram)
	for {
		id, p, err := readFrame(br, buf)
		if err != nil {
			return
		}
		mu.Lock()
		f := flows[id]
		mu.Unlock()
		if f == nil {
			// Only this goroutine adds flows, so dialing without the lock
			// can't race another add of the same id.
			c, err := net.Dial("udp", addr)
			if err != nil {
				continue
			}
			f = &farFlow{c: c.(*net.UDPConn)}
			f.last.Store(time.Now().UnixNano())
			mu.Lock()
			if len(flows) >= maxUDPFlows {
				var oid uint32
				var oldest *farFlow
				for k, x := range flows {
					if oldest == nil || x.last.Load() < oldest.last.Load() {
						oid, oldest = k, x
					}
				}
				drop(oid, oldest)
			}
			flows[id] = f
			mu.Unlock()
			go func(id uint32, f *farFlow) {
				rb := make([]byte, maxDatagram)
				for {
					n, err := f.c.Read(rb)
					if err != nil {
						if errors.Is(err, net.ErrClosed) {
							return
						}
						// e.g. ECONNREFUSED from an ICMP port unreachable:
						// the target may come back.
						select {
						case <-done:
							return
						case <-time.After(10 * time.Millisecond):
						}
						continue
					}
					f.last.Store(time.Now().UnixNano())
					wmu.Lock()
					err = writeFrame(st, id, rb[:n])
					wmu.Unlock()
					if err != nil {
						st.Close()
						return
					}
				}
			}(id, f)
		}
		f.last.Store(time.Now().UnixNano())
		_, _ = f.c.Write(p)
	}
}

// pipeHalf copies both ways like Pipe, but passes a half-close through:
// when one side finishes sending, the other is told (FIN) and can still
// answer. Both are closed once both directions are done, or at once if
// either breaks.
func pipeHalf(a, b net.Conn) {
	errs := make(chan error, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			err = closeWrite(dst)
		}
		errs <- err
	}
	go cp(a, b)
	go cp(b, a)
	if err := <-errs; err != nil {
		a.Close()
		b.Close()
	}
	<-errs
	a.Close()
	b.Close()
}

// closeWrite ends c's sending side only.
func closeWrite(c net.Conn) error {
	switch x := c.(type) {
	case interface{ CloseWrite() error }:
		return x.CloseWrite()
	case *yamux.Stream:
		return x.Close() // yamux's Close sends FIN and keeps reading
	}
	return errNoHalfClose
}

var errNoHalfClose = errors.New("tunnel: connection can't half-close")
