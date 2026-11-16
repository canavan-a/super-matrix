package nixagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"main/tunnel"
)

// DefaultSocket is where the agent listens and the relay looks for it.
const DefaultSocket = "/run/supermatrix-nix/agent.sock"

// Listen opens the agent's socket: only root and group (the relay's
// group) may use it, since whatever can talk to the agent runs code as
// root.
func Listen(path, group string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(path, 0, gid); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// Serve takes relay connections on ln until ctx ends.
func (m *Manager) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go m.serveConn(ctx, c)
	}
}

func (m *Manager) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), tunnel.MaxControlLine)
	if !sc.Scan() {
		return
	}
	var first msg
	if err := json.Unmarshal(sc.Bytes(), &first); err != nil {
		return
	}
	if first.Type == "logs" {
		m.serveLogs(ctx, c, first)
		return
	}

	s := m.subscribe()
	defer m.unsubscribe(s)
	var wmu sync.Mutex
	go func() {
		for mm := range s.ch {
			wmu.Lock()
			_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := writeMsg(c, mm)
			wmu.Unlock()
			if err != nil {
				break
			}
		}
		c.Close() // fell behind or broke: the relay reconnects
	}()
	handle := func(mm msg) {
		switch mm.Type {
		case "set":
			m.Set(mm.Services)
		case "action":
			if mm.Action != nil {
				m.Action(*mm.Action)
			}
		}
	}
	handle(first)
	for sc.Scan() {
		var mm msg
		if json.Unmarshal(sc.Bytes(), &mm) == nil {
			handle(mm)
		}
	}
}

func (m *Manager) serveLogs(ctx context.Context, c net.Conn, req msg) {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logs := tunnel.NixLogs{}
	if req.Logs != nil {
		logs = *req.Logs
	}
	rc, err := m.Logs(lctx, req.ID, logs)
	if err != nil {
		_ = writeMsg(c, msg{Type: "logs", Error: err.Error()})
		return
	}
	defer rc.Close()
	if writeMsg(c, msg{Type: "logs"}) != nil {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, c)
		cancel()
		rc.Close()
	}()
	_, _ = io.Copy(c, rc)
}

// Client is the relay's side of the agent's socket (a tunnel.NixAgent).
// It keeps reconnecting, and gives the agent the last set again each time.
type Client struct {
	Socket string
	Logf   func(format string, args ...any)

	mu      sync.Mutex
	conn    net.Conn
	host    *tunnel.NixHost
	set     []tunnel.NixService
	haveSet bool
	pending tunnel.NixReport
	notify  chan struct{}
	wmu     sync.Mutex
	// down is the last reason the agent couldn't be reached (logged once).
	down string
}

func NewClient(socket string, logf func(string, ...any)) *Client {
	if socket == "" {
		socket = DefaultSocket
	}
	return &Client{Socket: socket, Logf: logf, notify: make(chan struct{}, 1)}
}

func (c *Client) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// Run connects to the agent and reconnects when it goes away, until ctx
// ends.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", c.Socket)
		if err != nil {
			if why := err.Error(); why != c.down {
				c.down = why
				c.Logf("relay: no Nix agent at %s (%v); Nix services are off until it answers", c.Socket, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		c.down = ""
		c.Logf("relay: connected to the Nix agent at %s", c.Socket)
		c.serve(ctx, conn)
		c.Logf("relay: lost the Nix agent")
	}
}

func (c *Client) serve(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// Under wmu: a Set racing this goes out after the set sent here, never
	// before it (which would leave the agent on the older one).
	c.wmu.Lock()
	c.mu.Lock()
	c.conn = conn
	set, have := c.set, c.haveSet
	c.mu.Unlock()
	// The first line says this is a control connection (logs get their
	// own), so the agent answers even before there's a set to send.
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_ = writeMsg(conn, msg{Type: "hello"})
	if have {
		_ = writeMsg(conn, msg{Type: "set", Services: set})
	}
	c.wmu.Unlock()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), tunnel.MaxControlLine)
	for sc.Scan() {
		var mm msg
		if json.Unmarshal(sc.Bytes(), &mm) != nil {
			continue
		}
		c.mu.Lock()
		switch mm.Type {
		case "host":
			c.host = mm.Host
			c.pending.HostChanged = true
		case "status":
			c.pending.Status = append(c.pending.Status, mm.Status)
			if len(c.pending.Status) > 2000 {
				// Nobody's taking them (the server's away); the next set
				// brings a full snapshot anyway.
				c.pending.Status = c.pending.Status[1000:]
			}
		case "lock":
			if mm.Lock != nil {
				c.pending.Locks = append(c.pending.Locks, *mm.Lock)
			}
		case "known":
			c.pending.Known, c.pending.HasKnown = mm.Known, true
			if c.pending.Known == nil {
				c.pending.Known = []string{}
			}
		}
		c.mu.Unlock()
		c.signal()
	}
	conn.Close()
	c.mu.Lock()
	c.conn = nil
	c.host = nil
	c.pending.HostChanged = true
	c.mu.Unlock()
	c.signal()
}

func (c *Client) write(mm msg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("no Nix agent")
	}
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return writeMsg(conn, mm)
}

func (c *Client) Host() *tunnel.NixHost {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.host == nil {
		return nil
	}
	h := *c.host
	return &h
}

func (c *Client) Set(set []tunnel.NixService) {
	c.mu.Lock()
	c.set, c.haveSet = set, true
	c.mu.Unlock()
	_ = c.write(msg{Type: "set", Services: set})
}

func (c *Client) Action(act tunnel.NixAction) { _ = c.write(msg{Type: "action", Action: &act}) }

func (c *Client) Notify() <-chan struct{} { return c.notify }

func (c *Client) Take() tunnel.NixReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.pending
	c.pending = tunnel.NixReport{}
	return r
}

// Logs opens a connection of its own to the agent for a service's logs.
func (c *Client) Logs(ctx context.Context, id string, req tunnel.NixLogs) (io.ReadCloser, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, errors.New("the Nix agent isn't answering")
	}
	if err := writeMsg(conn, msg{Type: "logs", ID: id, Logs: &req}); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	// Byte by byte: what follows the reply is the log itself.
	var line []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, one); err != nil {
			conn.Close()
			return nil, fmt.Errorf("the Nix agent: %w", err)
		}
		if one[0] == '\n' {
			break
		}
		line = append(line, one[0])
		if len(line) > 64<<10 {
			conn.Close()
			return nil, errors.New("the Nix agent's reply is too long")
		}
	}
	var rep msg
	if err := json.Unmarshal(line, &rep); err != nil {
		conn.Close()
		return nil, err
	}
	if rep.Error != "" {
		conn.Close()
		return nil, errors.New(rep.Error)
	}
	_ = conn.SetReadDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	return &ctxConn{Conn: conn, stop: stop}, nil
}

type ctxConn struct {
	net.Conn
	stop func() bool
}

func (c *ctxConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

var _ tunnel.NixAgent = (*Client)(nil)

// Requeue puts back what the relay took but couldn't send, ahead of
// anything reported since, for the next session.
func (c *Client) Requeue(r tunnel.NixReport) {
	c.mu.Lock()
	c.pending.Status = append(r.Status, c.pending.Status...)
	c.pending.Locks = append(r.Locks, c.pending.Locks...)
	if r.HasKnown && !c.pending.HasKnown {
		c.pending.Known, c.pending.HasKnown = r.Known, true
	}
	c.pending.HostChanged = c.pending.HostChanged || r.HostChanged
	c.mu.Unlock()
	c.signal()
}
