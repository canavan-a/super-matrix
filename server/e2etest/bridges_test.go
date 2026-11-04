package e2etest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"main/config"
	"main/tunnel"
)

// tcpEcho is a TCP service that echoes each line back as "echo:<line>".
func tcpEcho(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					fmt.Fprintf(c, "echo:%s\n", s.Text())
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// udpEcho is a UDP service that answers every packet with "echo:<packet>".
func udpEcho(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(append([]byte("echo:"), buf[:n]...), from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// freePort finds a port that's free right now on 127.0.0.1.
func freePort(t *testing.T, proto string) int {
	t.Helper()
	if proto == "udp" {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		return pc.LocalAddr().(*net.UDPAddr).Port
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type bridge struct {
	ID            string
	ListenPort    int
	PublicAddress string
	ListenError   string
}

// bridgeRelay starts a relay and waits until the server has its features.
func (e *env) bridgeRelay(name string, opt func(*tunnel.Relay)) (string, *tunnel.Relay) {
	e.t.Helper()
	id, stop, r := e.startRelayWith(name, opt)
	e.t.Cleanup(stop)
	if opt == nil {
		e.waitFor(func() bool {
			var rs []struct {
				ID       string
				Features []string
			}
			e.api("GET", "/v1/relays", nil, 200, &rs)
			for _, x := range rs {
				if x.ID == id {
					return slices.Contains(x.Features, tunnel.FeatureBridgeUDP)
				}
			}
			return false
		})
	}
	return id, r
}

// tcpRoundTrip dials addr until the bridge is up and checks one echo.
func tcpRoundTrip(t *testing.T, addr, msg string) error {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(c, "%s\n", msg); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if line != "echo:"+msg+"\n" {
		return fmt.Errorf("got %q", line)
	}
	return nil
}

func (e *env) waitTCP(addr string) {
	e.t.Helper()
	e.waitFor(func() bool { return tcpRoundTrip(e.t, addr, "hello") == nil })
}

func udpRoundTrip(c *net.UDPConn, msg string) error {
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != "echo:"+msg {
		return fmt.Errorf("got %q", buf[:n])
	}
	return nil
}

func dialUDP(t *testing.T, port int) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func testBridgeTCP(t *testing.T, quic bool) {
	e := newEnv(t)
	if quic {
		e.srv.SetQUIC(e.listenQUIC())
	}
	a, _ := e.bridgeRelay("a", nil)
	b, _ := e.bridgeRelay("b", nil)
	if quic {
		e.waitTransport(a, tunnel.TransportQUIC)
		e.waitTransport(b, tunnel.TransportQUIC)
	}
	target := tcpEcho(t)
	port := freePort(t, "tcp")
	var br bridge
	e.api("POST", "/v1/bridges", map[string]any{
		"name": "db", "protocol": "tcp", "fromRelayId": a, "listenPort": port,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target,
	}, 201, &br)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	e.waitTCP(addr)

	// Many connections at once through the one bridge.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- tcpRoundTrip(t, addr, fmt.Sprintf("conn-%d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	// A client that half-closes after its request still gets the answer.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "half\n")
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	conn.Close()
	if err != nil || string(got) != "echo:half\n" {
		t.Fatalf("after half-close: %q %v", got, err)
	}

	// Both ends report the bridge healthy.
	e.waitFor(func() bool {
		sa, _ := e.relayState(a)
		sb, _ := e.relayState(b)
		return sa.Health[br.ID] == "ok" && sb.Health[br.ID] == "ok"
	})
}

func TestBridgeTCPWebSocket(t *testing.T) { testBridgeTCP(t, false) }
func TestBridgeTCPQUIC(t *testing.T)      { testBridgeTCP(t, true) }

func TestBridgeManyRelaysOneTarget(t *testing.T) {
	e := newEnv(t)
	a, _ := e.bridgeRelay("a", nil)
	c, _ := e.bridgeRelay("c", nil)
	b, _ := e.bridgeRelay("b", nil)
	target := tcpEcho(t)
	pa, pc := freePort(t, "tcp"), freePort(t, "tcp")
	var ba, bc bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "pg-a", "protocol": "tcp", "fromRelayId": a, "listenPort": pa,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 201, &ba)
	e.api("POST", "/v1/bridges", map[string]any{"name": "pg-c", "protocol": "tcp", "fromRelayId": c, "listenPort": pc,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 201, &bc)
	e.waitTCP(fmt.Sprintf("127.0.0.1:%d", pa))
	e.waitTCP(fmt.Sprintf("127.0.0.1:%d", pc))

	// Turning one off closes its port and leaves the other alone.
	e.api("PATCH", "/v1/bridges/"+ba.ID, map[string]any{"enabled": false}, 200, nil)
	e.waitFor(func() bool {
		_, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", pa), time.Second)
		return err != nil
	})
	if err := tcpRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", pc), "still"); err != nil {
		t.Fatal(err)
	}

	// A relay with bridges can't be deleted.
	e.api("DELETE", "/v1/relays/"+b, nil, 409, nil)
	// Deleting a bridge closes its port.
	e.api("DELETE", "/v1/bridges/"+bc.ID, nil, 200, nil)
	e.waitFor(func() bool {
		_, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", pc), time.Second)
		return err != nil
	})
}

func TestBridgeUDP(t *testing.T) {
	e := newEnv(t)
	a, _ := e.bridgeRelay("a", nil)
	b, _ := e.bridgeRelay("b", nil)
	target := udpEcho(t)
	port := freePort(t, "udp")
	e.api("POST", "/v1/bridges", map[string]any{"name": "turn", "protocol": "udp", "fromRelayId": a, "listenPort": port,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 201, nil)

	senders := []*net.UDPConn{dialUDP(t, port), dialUDP(t, port), dialUDP(t, port)}
	e.waitFor(func() bool { return udpRoundTrip(senders[0], "first") == nil })
	for round := range 5 {
		for i, s := range senders {
			msg := fmt.Sprintf("sender-%d-%d", i, round)
			if err := udpRoundTrip(s, msg); err != nil {
				t.Fatalf("%s: %v", msg, err)
			}
		}
	}
	// A packet near the largest UDP payload goes through whole.
	big := strings.Repeat("x", 60000)
	if err := udpRoundTrip(senders[1], big); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeRefusals(t *testing.T) {
	e := newEnv(t)
	a, _ := e.bridgeRelay("a", nil)
	b, _ := e.bridgeRelay("b", nil)
	c, rc := e.bridgeRelay("c", nil)
	target := tcpEcho(t)

	// The port is taken on a's machine: the bridge is made, a reports why
	// it can't listen.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	var br bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "x", "protocol": "tcp", "fromRelayId": a,
		"listenPort": taken.Addr().(*net.TCPAddr).Port, "toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 201, &br)
	e.waitFor(func() bool {
		s, _ := e.relayState(a)
		return strings.HasPrefix(s.Health[br.ID], "listen: ")
	})

	// Another relay can't use a's bridge.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = rc.OpenBridge(ctx, br.ID)
	var te *tunnel.TargetError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "no such bridge") {
		t.Fatalf("relay c opened a's bridge: %v", err)
	}
	_ = c

	// Same listen port twice on one relay; one relay to itself; bad input.
	e.api("POST", "/v1/bridges", map[string]any{"name": "y", "protocol": "tcp", "fromRelayId": a,
		"listenPort": taken.Addr().(*net.TCPAddr).Port, "toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 409, nil)
	e.api("POST", "/v1/bridges", map[string]any{"name": "z", "protocol": "tcp", "fromRelayId": a,
		"listenPort": 1, "toRelayId": a, "targetHost": "127.0.0.1", "targetPort": target}, 400, nil)
	e.api("POST", "/v1/bridges", map[string]any{"name": "z", "protocol": "sctp", "fromRelayId": a,
		"listenPort": 1, "toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 400, nil)
	// Public bridges are off on this server.
	e.api("POST", "/v1/bridges", map[string]any{"name": "p", "protocol": "tcp", "public": true,
		"listenPort": 20000, "toRelayId": b, "targetHost": "127.0.0.1", "targetPort": target}, 400, nil)
}

func TestBridgeOldRelay(t *testing.T) {
	e := newEnv(t)
	a, _ := e.bridgeRelay("a", nil)
	old, _ := e.bridgeRelay("old", func(r *tunnel.Relay) { r.Features = []string{} })
	e.waitFor(func() bool {
		var rs []struct {
			ID       string
			Version  string
			Features []string
		}
		e.api("GET", "/v1/relays", nil, 200, &rs)
		for _, x := range rs {
			if x.ID == old {
				return x.Version == "test" && len(x.Features) == 0
			}
		}
		return false
	})
	for _, body := range []map[string]any{
		{"name": "to-old", "protocol": "tcp", "fromRelayId": a, "listenPort": freePort(t, "tcp"), "toRelayId": old, "targetHost": "127.0.0.1", "targetPort": 1},
		{"name": "from-old", "protocol": "tcp", "fromRelayId": old, "listenPort": freePort(t, "tcp"), "toRelayId": a, "targetHost": "127.0.0.1", "targetPort": 1},
	} {
		e.api("POST", "/v1/bridges", body, 409, nil)
	}
	// A disabled bridge can be set up ahead of the update, but not turned on.
	var br bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "later", "protocol": "udp", "enabled": false, "fromRelayId": a,
		"listenPort": freePort(t, "udp"), "toRelayId": old, "targetHost": "127.0.0.1", "targetPort": 1}, 201, &br)
	e.api("PATCH", "/v1/bridges/"+br.ID, map[string]any{"enabled": true}, 409, nil)
}

func TestBridgePublic(t *testing.T) {
	tcpPort, udpPort := freePort(t, "tcp"), freePort(t, "udp")
	e := newEnvWith(t, func(c *config.Config) {
		c.PublicPorts = config.PortSet{{Lo: tcpPort, Hi: tcpPort}, {Lo: udpPort, Hi: udpPort}}
	})
	b, _ := e.bridgeRelay("b", nil)

	var tb bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "web", "protocol": "tcp", "public": true, "listenPort": tcpPort,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": tcpEcho(t)}, 201, &tb)
	if tb.PublicAddress != fmt.Sprintf("127.0.0.1:%d", tcpPort) {
		t.Fatalf("public address %q", tb.PublicAddress)
	}
	e.waitTCP(fmt.Sprintf("127.0.0.1:%d", tcpPort))

	var ub bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "turn", "protocol": "udp", "public": true, "listenPort": udpPort,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": udpEcho(t)}, 201, &ub)
	s1, s2 := dialUDP(t, udpPort), dialUDP(t, udpPort)
	e.waitFor(func() bool { return udpRoundTrip(s1, "one") == nil })
	if err := udpRoundTrip(s2, "two"); err != nil {
		t.Fatal(err)
	}

	// A DNS name for the public UDP bridge, on the wildcard domain.
	var doms []struct {
		ID       string
		Wildcard bool
	}
	e.api("GET", "/v1/domains", nil, 200, &doms)
	var wild string
	for _, d := range doms {
		if d.Wildcard {
			wild = d.ID
		}
	}
	var named struct {
		ID            string
		Host          string
		PublicAddress string
	}
	e.api("PATCH", "/v1/bridges/"+ub.ID, map[string]any{"domainId": wild, "subdomain": "Turn"}, 200, &named)
	if named.Host != "turn.test" || named.PublicAddress != fmt.Sprintf("turn.test:%d", udpPort) {
		t.Fatalf("named public bridge: %+v", named)
	}
	e.api("PATCH", "/v1/bridges/"+ub.ID, map[string]any{"subdomain": "not a label"}, 400, nil)
	// The domain can't go while a bridge is named on it.
	e.api("DELETE", "/v1/domains/"+wild, nil, 409, nil)
	// The name resolves to this server (the lookup is faked here).
	e.srv.Lookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	var dns struct {
		OK    bool
		Probe string
	}
	e.api("GET", "/v1/bridges/"+ub.ID+"/dns", nil, 200, &dns)
	if !dns.OK || dns.Probe != "turn.test" {
		t.Fatalf("dns check: %+v", dns)
	}
	// Traffic is still told apart by port alone.
	if err := udpRoundTrip(s1, "named"); err != nil {
		t.Fatal(err)
	}

	// Outside the range, or a port already used by another public bridge.
	e.api("POST", "/v1/bridges", map[string]any{"name": "x", "protocol": "tcp", "public": true, "listenPort": 1,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": 1}, 400, nil)
	e.api("POST", "/v1/bridges", map[string]any{"name": "x", "protocol": "tcp", "public": true, "listenPort": tcpPort,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": 1}, 409, nil)

	// An allow list that leaves out 127.0.0.1 shuts us out.
	e.api("PATCH", "/v1/bridges/"+tb.ID, map[string]any{"ipAllowlist": []string{"10.0.0.0/8"}}, 200, nil)
	e.waitFor(func() bool { return tcpRoundTrip(t, fmt.Sprintf("127.0.0.1:%d", tcpPort), "blocked") != nil })
	e.api("PATCH", "/v1/bridges/"+tb.ID, map[string]any{"ipAllowlist": []string{"127.0.0.1"}}, 200, nil)
	e.waitTCP(fmt.Sprintf("127.0.0.1:%d", tcpPort))

	// Turning the bridge off closes the server's port.
	e.api("PATCH", "/v1/bridges/"+tb.ID, map[string]any{"enabled": false}, 200, nil)
	e.waitFor(func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort), time.Second)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
}

func TestBridgePublicPortTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port
	e := newEnvWith(t, func(c *config.Config) { c.PublicPorts = config.PortSet{{Lo: port, Hi: port}} })
	b, _ := e.bridgeRelay("b", nil)
	var br bridge
	e.api("POST", "/v1/bridges", map[string]any{"name": "x", "protocol": "tcp", "public": true, "listenPort": port,
		"toRelayId": b, "targetHost": "127.0.0.1", "targetPort": 1}, 201, &br)
	if !strings.HasPrefix(br.ListenError, "listen: ") {
		t.Fatalf("listenError %q", br.ListenError)
	}
}
