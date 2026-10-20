package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

type Config struct {
	// AdminHost serves the API, the relay endpoint and the web app, e.g.
	// matrix.example.com.
	AdminHost string
	// BaseDomain, if set, becomes the first wildcard domain (*.BaseDomain)
	// when the server has none yet. Domains are managed in the app after that.
	BaseDomain string
	// TLS is "acme" (Let's Encrypt, one cert per host) or "off" (plain HTTP
	// on HTTPAddr only, for dev or behind another TLS proxy).
	TLS       string
	HTTPAddr  string
	HTTPSAddr string
	// ACMEEmail is optional; Let's Encrypt uses it for expiry notices.
	ACMEEmail string
	// ACMEDirectory overrides the ACME directory URL (e.g. LE staging).
	ACMEDirectory string
	DataDir       string
	DBPath        string
	CertDir       string
	// RootPassword is read once at startup from SUPERMATRIX_ROOT_PASSWORD_FILE
	// (or SUPERMATRIX_ROOT_PASSWORD, for dev).
	RootPassword string
	// StaticDir, when set, serves the built web client on the admin host.
	StaticDir string
	// TrustProxy believes the rightmost X-Forwarded-For on loopback
	// connections. See guard.ClientIP.
	TrustProxy bool
	// QUIC is on unless SUPERMATRIX_QUIC=off: relays connect over QUIC on
	// QUICAddr (UDP; by default the same port as HTTPS, or as HTTP with TLS
	// off) and fall back to the WebSocket.
	QUIC     bool
	QUICAddr string
	// QUICPublic is the host:port relays are told to dial (default: the
	// admin host and QUICAddr's port). Set it when a NAT or firewall maps a
	// different port.
	QUICPublic string
}

func (c Config) TLSOn() bool { return c.TLS != "off" }

func Load() (Config, error) {
	dataDir := getEnv("SUPERMATRIX_DATA_DIR", ".data")
	c := Config{
		AdminHost:     strings.ToLower(os.Getenv("SUPERMATRIX_ADMIN_HOST")),
		BaseDomain:    strings.ToLower(strings.TrimPrefix(os.Getenv("SUPERMATRIX_BASE_DOMAIN"), "*.")),
		TLS:           getEnv("SUPERMATRIX_TLS", "acme"),
		HTTPAddr:      getEnv("SUPERMATRIX_HTTP_ADDR", ":80"),
		HTTPSAddr:     getEnv("SUPERMATRIX_HTTPS_ADDR", ":443"),
		ACMEEmail:     os.Getenv("SUPERMATRIX_ACME_EMAIL"),
		ACMEDirectory: os.Getenv("SUPERMATRIX_ACME_DIRECTORY"),
		DataDir:       dataDir,
		DBPath:        getEnv("SUPERMATRIX_DB_PATH", filepath.Join(dataDir, "supermatrix.db")),
		CertDir:       filepath.Join(dataDir, "certs"),
		StaticDir:     os.Getenv("SUPERMATRIX_STATIC_DIR"),
		TrustProxy:    os.Getenv("SUPERMATRIX_TRUST_PROXY") == "1",
		QUIC:          os.Getenv("SUPERMATRIX_QUIC") != "off",
		QUICAddr:      os.Getenv("SUPERMATRIX_QUIC_ADDR"),
		QUICPublic:    os.Getenv("SUPERMATRIX_QUIC_PUBLIC"),
	}
	if c.QUICAddr == "" {
		c.QUICAddr = c.HTTPSAddr
		if !c.TLSOn() {
			c.QUICAddr = c.HTTPAddr
		}
	}
	if c.ACMEDirectory == "staging" {
		c.ACMEDirectory = LetsEncryptStaging
	}

	if f := os.Getenv("SUPERMATRIX_ROOT_PASSWORD_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, fmt.Errorf("reading SUPERMATRIX_ROOT_PASSWORD_FILE: %w", err)
		}
		c.RootPassword = strings.TrimRight(string(b), "\r\n")
	} else {
		c.RootPassword = os.Getenv("SUPERMATRIX_ROOT_PASSWORD")
	}

	switch {
	case c.AdminHost == "":
		return c, errors.New("SUPERMATRIX_ADMIN_HOST is required")
	case c.TLS != "acme" && c.TLS != "off":
		return c, errors.New(`SUPERMATRIX_TLS must be "acme" or "off"`)
	case c.RootPassword == "":
		return c, errors.New("set SUPERMATRIX_ROOT_PASSWORD_FILE (the root password)")
	}
	return c, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// QUICEndpoint is the host:port relays dial for QUIC.
func (c Config) QUICEndpoint() string {
	if c.QUICPublic != "" {
		return c.QUICPublic
	}
	_, port, err := net.SplitHostPort(c.QUICAddr)
	if err != nil {
		port = "443"
	}
	return net.JoinHostPort(c.AdminHost, port)
}
