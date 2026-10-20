// Package tunnel is the link between the server and its relays (see
// docs/protocol.md).
//
// A relay connects over QUIC when it can (UDP, ALPN "supermatrix-relay/1",
// the token on the first stream), and otherwise over the server's
// /v1/relay/connect WebSocket with yamux on top. Either way the session has
// one control stream (newline-delimited JSON, both directions) and one
// stream per proxied connection, opened by the server. A data stream starts
// with a StreamHeader line naming the deployment; the relay answers with a
// StreamReply line and, if it could reach the target, the stream becomes a
// raw byte pipe to it.
//
// The relay only ever dials targets the server has pushed to it over the
// control channel, keyed by deployment ID; a stream can't name an address.
package tunnel

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// Target is one deployment as the relay sees it: where to forward.
type Target struct {
	ID            string `json:"id"`
	Protocol      string `json:"protocol"` // http | https
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TLSSkipVerify bool   `json:"tlsSkipVerify,omitempty"`
	SNI           string `json:"sni,omitempty"`
}

// Hello is what a relay says about itself when it connects.
type Hello struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	// Fallback says why a relay on the WebSocket isn't using QUIC: the
	// QUIC error, or that it's configured or proxied that way.
	Fallback string `json:"fallback,omitempty"`
}

// ALPN is the TLS protocol name relays use on the server's QUIC port.
const ALPN = "supermatrix-relay/1"

// QUICAuth is the first line on a QUIC session's first stream (opened by
// the relay). The server answers with an AuthReply line; on success the
// same stream carries the control channel from then on.
type QUICAuth struct {
	Token string `json:"token"`
}

// AuthReply answers QUICAuth; an empty Error means the relay is in.
type AuthReply struct {
	Error string `json:"error,omitempty"`
}

// Transports is GET /v1/relay/transports: how this server can be reached.
// QUIC is nil when the server has it off.
type Transports struct {
	QUIC *QUICEndpoint `json:"quic"`
}

// QUICEndpoint is where the server listens for relays over QUIC.
type QUICEndpoint struct {
	// Addr is host:port (UDP).
	Addr string `json:"addr"`
	// ServerName is the name its certificate is checked against.
	ServerName string `json:"serverName"`
	// Pin, when set, is "sha256:<hex>" of the certificate: the server has no
	// public certificate (TLS off) and uses a self-signed one.
	Pin string `json:"pin,omitempty"`
}

// Control is one control-channel message.
//
//	server → relay  {"type":"targets","targets":[…]}
//	relay → server  {"type":"hello","hello":{…}}
//	relay → server  {"type":"health","health":{"<deployment>":"ok"|"<error>"}}
type Control struct {
	Type    string            `json:"type"`
	Targets []Target          `json:"targets,omitempty"`
	Hello   *Hello            `json:"hello,omitempty"`
	Health  map[string]string `json:"health,omitempty"`
}

// StreamHeader opens a data stream.
type StreamHeader struct {
	Deployment string `json:"d"`
}

// StreamReply answers a StreamHeader; an empty Error means the target is
// connected and raw bytes follow.
type StreamReply struct {
	Error string `json:"error,omitempty"`
}

func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	// The default 256 KiB window caps one stream at 256 KiB per round trip
	// (about 5 MB/s at 50 ms).
	c.MaxStreamWindowSize = 4 << 20
	c.KeepAliveInterval = 20 * time.Second
	c.ConnectionWriteTimeout = 15 * time.Second
	c.LogOutput = io.Discard
	return c
}

// quicConfig is shared by both ends; the server only accepts the relay's
// one control stream, the relay accepts as many data streams as come.
func quicConfig(incomingStreams int64) *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout:           5 * time.Second,
		MaxIdleTimeout:                 30 * time.Second,
		KeepAlivePeriod:                10 * time.Second,
		MaxIncomingStreams:             incomingStreams,
		MaxIncomingUniStreams:          -1,
		InitialStreamReceiveWindow:     512 << 10,
		MaxStreamReceiveWindow:         16 << 20,
		InitialConnectionReceiveWindow: 1 << 20,
		MaxConnectionReceiveWindow:     64 << 20,
	}
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readLine reads one JSON line byte by byte, so nothing past the newline is
// consumed from a stream that becomes a raw pipe afterwards.
func readLine(r io.Reader, v any) error {
	var buf []byte
	one := make([]byte, 1)
	for len(buf) < 4096 {
		if _, err := io.ReadFull(r, one); err != nil {
			return err
		}
		if one[0] == '\n' {
			return json.Unmarshal(buf, v)
		}
		buf = append(buf, one[0])
	}
	return errors.New("tunnel: header line too long")
}

// lineReader reads newline-delimited JSON from a stream that stays JSON.
type lineReader struct{ s *bufio.Scanner }

func newLineReader(r io.Reader) *lineReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), 4<<20)
	return &lineReader{s: s}
}

func (l *lineReader) next(v any) error {
	if !l.s.Scan() {
		if err := l.s.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(l.s.Bytes(), v)
}
