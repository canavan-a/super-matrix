package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	// SMTP sends the sign-in codes for email-code deployments, as in
	// super-atlas: SMTPHost/SMTPPort default to Gmail, SMTPEmail is both
	// the login and the From address, SMTPAppPassword its app password.
	SMTPHost        string
	SMTPPort        int
	SMTPEmail       string
	SMTPAppPassword string
	// PublicPorts is where public bridges may listen (SUPERMATRIX_PUBLIC_PORTS,
	// e.g. "20000-20100,3478"); empty turns public bridges off.
	PublicPorts PortSet
	// PublicAddr is the address public bridges bind (default: all).
	PublicAddr string
}

func (c Config) TLSOn() bool { return c.TLS != "off" }

// SMTPOn reports whether sign-in codes can be emailed.
func (c Config) SMTPOn() bool { return c.SMTPEmail != "" && c.SMTPAppPassword != "" }

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
		PublicAddr:    os.Getenv("SUPERMATRIX_PUBLIC_ADDR"),

		SMTPHost:        getEnv("SUPERMATRIX_SMTP_HOST", "smtp.gmail.com"),
		SMTPEmail:       os.Getenv("SUPERMATRIX_SMTP_EMAIL"),
		SMTPAppPassword: os.Getenv("SUPERMATRIX_SMTP_APP_PASSWORD"),
	}
	port, err := strconv.Atoi(getEnv("SUPERMATRIX_SMTP_PORT", "587"))
	if err != nil || port < 1 || port > 65535 {
		return c, errors.New("SUPERMATRIX_SMTP_PORT must be a port number")
	}
	c.SMTPPort = port
	if c.PublicPorts, err = ParsePortSet(os.Getenv("SUPERMATRIX_PUBLIC_PORTS")); err != nil {
		return c, fmt.Errorf("SUPERMATRIX_PUBLIC_PORTS: %w", err)
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

// PortSet is a list of port ranges, e.g. "20000-20100,3478".
type PortSet []PortRange

type PortRange struct{ Lo, Hi int }

// ParsePortSet parses comma-separated ports and lo-hi ranges.
func ParsePortSet(s string) (PortSet, error) {
	var out PortSet
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(f, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		if err == nil && isRange {
			b, err = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err != nil || a < 1 || b > 65535 || a > b {
			return nil, fmt.Errorf("bad port or range %q", f)
		}
		out = append(out, PortRange{a, b})
	}
	return out, nil
}

// Contains reports whether p is in the set.
func (s PortSet) Contains(p int) bool {
	for _, r := range s {
		if p >= r.Lo && p <= r.Hi {
			return true
		}
	}
	return false
}

func (s PortSet) String() string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		if r.Lo == r.Hi {
			parts = append(parts, strconv.Itoa(r.Lo))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.Lo, r.Hi))
		}
	}
	return strings.Join(parts, ",")
}

// Reserved reports whether the server itself uses port (tcp or udp), so
// a public bridge can't have it.
func (c Config) Reserved(protocol string, port int) bool {
	portOf := func(addr string) int {
		_, p, err := net.SplitHostPort(addr)
		if err != nil {
			return -1
		}
		n, _ := strconv.Atoi(p)
		return n
	}
	if protocol == "tcp" {
		return port == portOf(c.HTTPAddr) || (c.TLSOn() && port == portOf(c.HTTPSAddr))
	}
	return c.QUIC && port == portOf(c.QUICAddr)
}
