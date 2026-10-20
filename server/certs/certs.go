// Package certs gets a Let's Encrypt certificate for every hostname the
// server answers for: the admin host and each enabled deployment. There is
// no wildcard cert; each host gets its own, issued on first use (or when
// warmed after a deployment is created) and renewed by autocert.
package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"main/config"
	"main/guard"
)

// Manager wraps autocert with a host policy (only hosts we serve) and
// status reporting.
type Manager struct {
	m   *autocert.Manager
	cfg config.Config
	// Allowed reports whether host is an enabled deployment.
	allowed func(host string) bool
	// OnStatus is told the outcome of each warm-up.
	OnStatus func(host string, expires time.Time, err error)

	mu      sync.Mutex
	warming map[string]bool
}

func New(cfg config.Config, allowed func(host string) bool) *Manager {
	c := &Manager{cfg: cfg, allowed: allowed, warming: map[string]bool{}}
	c.m = &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.CertDir),
		HostPolicy: c.policy,
		Email:      cfg.ACMEEmail,
	}
	if cfg.ACMEDirectory != "" {
		c.m.Client = &acme.Client{DirectoryURL: cfg.ACMEDirectory}
	}
	return c
}

// policy refuses any name we don't serve, so a stray SNI or Host can't make
// us ask Let's Encrypt for certs (and burn the rate limit).
func (c *Manager) policy(_ context.Context, host string) error {
	if host == c.cfg.AdminHost || c.allowed(host) {
		return nil
	}
	return errors.New("certs: unknown host")
}

// TLSConfig is for the :443 listener; it also answers TLS-ALPN-01.
func (c *Manager) TLSConfig() *tls.Config {
	t := c.m.TLSConfig()
	t.MinVersion = tls.VersionTLS12
	return t
}

// HTTPHandler answers HTTP-01 challenges on :80 and sends everything else
// on known hosts to https.
func (c *Manager) HTTPHandler() http.Handler {
	return c.m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := guard.StripPort(r.Host)
		if host != c.cfg.AdminHost && !c.allowed(host) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	}))
}

// Warm gets (or loads) host's certificate in the background, so it's ready
// before the first visitor and its status shows up in the app.
func (c *Manager) Warm(host string) {
	c.mu.Lock()
	if c.warming[host] {
		c.mu.Unlock()
		return
	}
	c.warming[host] = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.warming, host)
			c.mu.Unlock()
		}()
		// Look like a modern client so the ECDSA cert (what browsers get)
		// is the one issued, not the RSA fallback.
		hello := &tls.ClientHelloInfo{
			ServerName:        host,
			SignatureSchemes:  []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves:   []tls.CurveID{tls.CurveP256},
			CipherSuites:      []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
		}
		cert, err := c.m.GetCertificate(hello)
		var exp time.Time
		if err == nil {
			leaf := cert.Leaf
			if leaf == nil && len(cert.Certificate) > 0 {
				leaf, err = x509.ParseCertificate(cert.Certificate[0])
			}
			if leaf != nil {
				exp = leaf.NotAfter
			}
		}
		if err != nil {
			log.Printf("certs: %s: %v", host, err)
		}
		if c.OnStatus != nil {
			c.OnStatus(host, exp, err)
		}
	}()
}
