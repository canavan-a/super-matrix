package e2etest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"main/certs"
	"main/tunnel"
)

// listenQUIC starts a QUIC listener for e's server and returns the
// endpoint relays should be told about (not yet advertised).
func (e *env) listenQUIC() *tunnel.QUICEndpoint {
	e.t.Helper()
	cert, err := certs.SelfSigned(e.dir+"/quic-cert.pem", "127.0.0.1")
	if err != nil {
		e.t.Fatal(err)
	}
	ln, err := tunnel.ListenQUIC("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { ln.Close() })
	go e.srv.ServeQUIC(ln)
	return &tunnel.QUICEndpoint{Addr: ln.Addr().String(), ServerName: "127.0.0.1", Pin: tunnel.CertPin(cert.Certificate[0])}
}

func (e *env) relayState(id string) (tunnel.RelayState, bool) {
	var st struct{ Relays map[string]tunnel.RelayState }
	e.api("GET", "/v1/status", nil, 200, &st)
	s, ok := st.Relays[id]
	return s, ok
}

func (e *env) waitTransport(id, transport string) tunnel.RelayState {
	e.t.Helper()
	var s tunnel.RelayState
	e.waitFor(func() bool {
		var ok bool
		s, ok = e.relayState(id)
		return ok && s.Transport == transport
	})
	return s
}

func (e *env) deploy(relayID, sub string) string {
	e.t.Helper()
	th, tp := target(e.t, false)
	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": sub, "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &d)
	return sub + ".test"
}

// upstreamWS opens a WebSocket to the echo target through a deployment.
func (e *env) upstreamWS(host string) *websocket.Conn {
	e.t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDial: func(string, string) (net.Conn, error) {
		return net.Dial("tcp", e.addr)
	}}
	c, _, err := dialer.Dial("ws://"+host+"/ws", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func echo(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	_, m, err := c.ReadMessage()
	if err != nil || string(m) != "echo:"+msg {
		t.Fatalf("echo: %q %v", m, err)
	}
}

func TestQUIC(t *testing.T) {
	e := newEnv(t)
	e.srv.SetQUIC(e.listenQUIC())
	id, stop, relay := e.startRelayWith("fast", nil)
	defer stop()
	s := e.waitTransport(id, tunnel.TransportQUIC)
	if s.Fallback != "" {
		t.Fatalf("fallback on QUIC: %q", s.Fallback)
	}
	if tr, _ := relay.Status(); tr != tunnel.TransportQUIC {
		t.Fatalf("relay says %q", tr)
	}
	host := e.deploy(id, "app")

	t.Run("http", func(t *testing.T) {
		res, body := e.get(host, "/hello", nil)
		if res.StatusCode != 200 || !strings.Contains(body, `"path":"/hello"`) {
			t.Fatalf("%d %s", res.StatusCode, body)
		}
	})
	t.Run("big download", func(t *testing.T) {
		const n = 32 << 20
		res := e.req("GET", host, "/big?n="+strconv.Itoa(n), nil, nil)
		got, err := io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if err != nil || got != n {
			t.Fatalf("got %d bytes, %v", got, err)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan string, 64)
		for i := range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := e.req("GET", host, "/c/"+strconv.Itoa(i), nil, nil)
				if b := readBody(t, res); !strings.Contains(b, "/c/"+strconv.Itoa(i)) {
					errs <- b
				}
			}()
		}
		wg.Wait()
		close(errs)
		for b := range errs {
			t.Fatal(b)
		}
	})
	t.Run("websocket upstream", func(t *testing.T) {
		c := e.upstreamWS(host)
		defer c.Close()
		echo(t, c, "over quic")
	})
	t.Run("visible", func(t *testing.T) {
		s, _ := e.relayState(id)
		if s.RTTMs <= 0 || s.BytesReceived < 32<<20 || s.Remote == "" {
			t.Fatalf("stats: %+v", s)
		}
		var rs []struct{ ID, Transport string }
		e.api("GET", "/v1/relays", nil, 200, &rs)
		if len(rs) != 1 || rs[0].Transport != tunnel.TransportQUIC {
			t.Fatalf("relays: %+v", rs)
		}
	})
	t.Run("rotate drops the QUIC session", func(t *testing.T) {
		e.api("POST", "/v1/relays/"+id+"/rotate", nil, 200, nil)
		e.waitFor(func() bool { _, ok := e.relayState(id); return !ok })
	})
}

func TestWebSocketByChoice(t *testing.T) {
	e := newEnv(t)
	e.srv.SetQUIC(e.listenQUIC())
	id, stop, _ := e.startRelayWith("slow", func(r *tunnel.Relay) { r.Transport = tunnel.TransportWebSocket })
	defer stop()
	s := e.waitTransport(id, tunnel.TransportWebSocket)
	e.waitFor(func() bool { s, _ = e.relayState(id); return s.Fallback != "" })
	if s.Fallback != "transport set to websocket" {
		t.Fatalf("fallback %q", s.Fallback)
	}
	res, body := e.get(e.deploy(id, "app"), "/", nil)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestQUICOffOnServer(t *testing.T) {
	e := newEnv(t)
	id, stop, _ := e.startRelayWith("r", nil)
	defer stop()
	e.waitTransport(id, tunnel.TransportWebSocket)
	e.waitFor(func() bool {
		s, _ := e.relayState(id)
		return s.Fallback == "the server has QUIC turned off"
	})
}

// UDP blocked: the relay falls back to the WebSocket, says why, and moves
// to QUIC once it gets through, without dropping connections in flight.
func TestQUICFallbackThenUpgrade(t *testing.T) {
	e := newEnv(t)
	real := e.listenQUIC()
	hole, err := net.ListenPacket("udp", "127.0.0.1:0") // swallows every packet
	if err != nil {
		t.Fatal(err)
	}
	defer hole.Close()
	blocked := *real
	blocked.Addr = hole.LocalAddr().String()
	e.srv.SetQUIC(&blocked)

	id, stop, relay := e.startRelayWith("cafe", func(r *tunnel.Relay) {
		r.QUICTimeout = 300 * time.Millisecond
		r.UpgradeEvery = 200 * time.Millisecond
	})
	defer stop()
	e.waitTransport(id, tunnel.TransportWebSocket)
	var s tunnel.RelayState
	e.waitFor(func() bool { s, _ = e.relayState(id); return strings.Contains(s.Fallback, "UDP blocked") })
	if _, why := relay.Status(); !strings.Contains(why, "UDP blocked") {
		t.Fatalf("relay fallback %q", why)
	}

	host := e.deploy(id, "app")
	if res, body := e.get(host, "/", nil); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	old := e.upstreamWS(host) // rides the WebSocket session
	defer old.Close()
	echo(t, old, "before")

	e.srv.SetQUIC(real)
	s = e.waitTransport(id, tunnel.TransportQUIC)
	if s.Fallback != "" {
		t.Fatalf("fallback after upgrade: %q", s.Fallback)
	}
	echo(t, old, "still here") // the old session drains, it isn't cut
	if res, body := e.get(host, "/after", nil); res.StatusCode != 200 || !strings.Contains(body, "/after") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	fresh := e.upstreamWS(host)
	defer fresh.Close()
	echo(t, fresh, "new")
}

// A QUIC relay with a bad token is refused on the QUIC handshake itself.
func TestQUICBadToken(t *testing.T) {
	e := newEnv(t)
	ep := e.listenQUIC()
	// Hand out the endpoint without checking the token, so the QUIC
	// listener is what has to refuse it.
	disc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tunnel.Transports{QUIC: ep})
	}))
	defer disc.Close()
	logs := make(chan string, 16)
	r := &tunnel.Relay{ServerURL: disc.URL, Token: "smr_nope", Transport: tunnel.TransportQUIC, HealthEvery: time.Hour,
		Logf: func(f string, a ...any) {
			select {
			case logs <- fmt.Sprintf(f, a...):
			default:
			}
		}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var msgs []string
		timeout := time.After(5 * time.Second)
		for {
			select {
			case m := <-logs:
				msgs = append(msgs, m)
				if strings.Contains(m, "disconnected") {
					if !strings.Contains(m, tunnel.ErrUnauthorized.Error()) {
						t.Errorf("want a token rejection: %s", m)
					}
					return
				}
			case <-timeout:
				t.Errorf("no disconnect: %v", msgs)
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	<-done
	if tr, _ := r.Status(); tr != "" {
		t.Fatalf("connected over %q with a bad token", tr)
	}
}
