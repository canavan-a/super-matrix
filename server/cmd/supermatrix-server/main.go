package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"

	"main/api"
	"main/certs"
	"main/config"
	"main/database"
	"main/edge"
	"main/registry"
	"main/tunnel"
	"main/version"
)

func main() {
	// .env is optional: local dev only; systemd sets the real environment.
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	db, err := database.Connect(cfg.DBPath)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	if err := database.Seed(db, cfg.BaseDomain); err != nil {
		log.Fatalf("seed domains: %v", err)
	}
	reg, err := registry.New(db, cfg)
	if err != nil {
		log.Fatalf("load deployments: %v", err)
	}
	key, err := edge.LoadKey(filepath.Join(cfg.DataDir, "cookie.key"))
	if err != nil {
		log.Fatalf("cookie key: %v", err)
	}

	hub := tunnel.NewHub()
	a := api.New(db, cfg, reg, hub)
	e := edge.New(cfg, reg, hub, key, a.Handler())
	e.TokenUsed = a.TokenUsed
	a.AfterChange = e.Changed

	srv := func(addr string, h http.Handler) *http.Server {
		// No write timeout: proxied responses can stream for as long as
		// they like (SSE, downloads, WebSockets).
		return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	}

	if !cfg.TLSOn() {
		cert, err := certs.SelfSigned(filepath.Join(cfg.DataDir, "quic-cert.pem"), cfg.AdminHost)
		if err != nil {
			log.Fatalf("quic certificate: %v", err)
		}
		startQUIC(cfg, a, &tls.Config{Certificates: []tls.Certificate{cert}}, tunnel.CertPin(cert.Certificate[0]))
		log.Printf("supermatrix %s: TLS off, listening on %s (admin host %s)", version.Version, cfg.HTTPAddr, cfg.AdminHost)
		log.Fatal(srv(cfg.HTTPAddr, e).ListenAndServe())
	}

	cm := certs.New(cfg, reg.HostAllowed)
	cm.OnStatus = a.CertStatus
	a.Certs = cm

	// Warm every host at startup (loads cached certs, issues missing ones),
	// then twice a day so expiry dates stay current in the app.
	warmAll := func() {
		cm.Warm(cfg.AdminHost)
		for _, h := range reg.Hosts() {
			cm.Warm(h)
		}
	}
	go func() {
		warmAll()
		for range time.Tick(12 * time.Hour) {
			warmAll()
		}
	}()

	go func() {
		log.Printf("supermatrix: ACME challenges + redirects on %s", cfg.HTTPAddr)
		log.Fatal(srv(cfg.HTTPAddr, cm.HTTPHandler()).ListenAndServe())
	}()
	startQUIC(cfg, a, cm.TLSConfig(), "")
	s := srv(cfg.HTTPSAddr, e)
	s.TLSConfig = cm.TLSConfig()
	log.Printf("supermatrix %s: listening on %s (admin host %s)", version.Version, cfg.HTTPSAddr, cfg.AdminHost)
	log.Fatal(s.ListenAndServeTLS("", ""))
}

// startQUIC listens for relays over QUIC. If the UDP port can't be had the
// server carries on without it: relays fall back to the WebSocket.
func startQUIC(cfg config.Config, a *api.API, tlsConf *tls.Config, pin string) {
	if !cfg.QUIC {
		log.Printf("supermatrix: QUIC off; relays use the WebSocket")
		return
	}
	ln, err := tunnel.ListenQUIC(cfg.QUICAddr, tlsConf)
	if err != nil {
		log.Printf("supermatrix: QUIC unavailable (%v); relays use the WebSocket", err)
		return
	}
	ep := &tunnel.QUICEndpoint{Addr: cfg.QUICEndpoint(), ServerName: cfg.AdminHost, Pin: pin}
	a.SetQUIC(ep)
	log.Printf("supermatrix: relays over QUIC on udp %s (advertised as %s)", ln.Addr(), ep.Addr)
	go func() { _ = a.ServeQUIC(ln) }()
}
