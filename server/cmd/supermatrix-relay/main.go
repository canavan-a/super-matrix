// Command supermatrix-relay runs on the device next to the services being
// exposed. It dials out to the Super Matrix server (no inbound ports) and
// forwards each deployment's traffic to its target. Which targets it serves
// is decided in the app; the relay only needs the server URL and its token.
//
//	SUPERMATRIX_SERVER_URL=https://matrix.example.com
//	SUPERMATRIX_RELAY_TOKEN_FILE=/run/credentials/…/token   (or SUPERMATRIX_RELAY_TOKEN)
//	SUPERMATRIX_TRANSPORT=auto        auto (QUIC, else the WebSocket) | quic | websocket
//	SUPERMATRIX_QUIC_ADDR=host:port   optional; overrides the QUIC address the server advertises
//	SUPERMATRIX_LOCAL_ADDR=127.0.0.1:3388   the local API ("off" turns it off)
//
// While it runs, the relay also serves a local API on 127.0.0.1:3388 with
// no auth, which passes requests on to the server with the relay's token.
// Once "Manage its deployments" is on for the relay in the app, the same
// binary is a CLI for that API: supermatrix-relay list | create | update |
// delete … (see cli.go). The relay can only touch deployments on itself.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"main/tunnel"
	"main/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] != "run" {
		os.Exit(cli(os.Args[1:]))
	}
	run()
}

func run() {
	_ = godotenv.Load()
	serverURL := strings.TrimRight(os.Getenv("SUPERMATRIX_SERVER_URL"), "/")
	if serverURL == "" {
		log.Fatal("SUPERMATRIX_SERVER_URL is required (the server's admin URL, e.g. https://matrix.example.com)")
	}
	token := os.Getenv("SUPERMATRIX_RELAY_TOKEN")
	if f := os.Getenv("SUPERMATRIX_RELAY_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("reading SUPERMATRIX_RELAY_TOKEN_FILE: %v", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		log.Fatal("set SUPERMATRIX_RELAY_TOKEN_FILE to the token the app showed when you added this relay")
	}

	transport := os.Getenv("SUPERMATRIX_TRANSPORT")
	switch transport {
	case "", "auto", tunnel.TransportQUIC, tunnel.TransportWebSocket:
	default:
		log.Fatalf("SUPERMATRIX_TRANSPORT must be auto, quic or websocket (got %q)", transport)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("supermatrix-relay %s → %s", version.Version, serverURL)

	if addr := localAddr(); addr != "off" {
		go func() {
			// The relay itself doesn't need it, so a failure here (say,
			// the port is taken) is only logged.
			if err := serveLocal(ctx, addr, serverURL, token); err != nil {
				log.Printf("local API on %s: %v", addr, err)
			}
		}()
	}

	r := &tunnel.Relay{ServerURL: serverURL, Token: token, Version: version.Version,
		Transport: transport, QUICAddr: os.Getenv("SUPERMATRIX_QUIC_ADDR")}
	_ = r.Run(ctx)
}

// localAddr is SUPERMATRIX_LOCAL_ADDR, or the default.
func localAddr() string {
	if a := strings.TrimSpace(os.Getenv("SUPERMATRIX_LOCAL_ADDR")); a != "" {
		return a
	}
	return DefaultLocalAddr
}
