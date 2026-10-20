package database

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
)

type Relay struct {
	ID         string `gorm:"primaryKey"`
	Name       string
	TokenHash  string
	CreatedAt  time.Time
	LastSeenAt *time.Time
	LastIP     string `gorm:"column:last_ip"`
	Version    string
	Hostname   string
	// Transport is how it last connected (quic | websocket), Fallback why
	// that wasn't QUIC.
	Transport string
	Fallback  string
}

// Auth modes a deployment can sit behind.
const (
	AuthNone  = "none"
	AuthLogin = "login"
	AuthBasic = "basic"
	AuthToken = "token"
)

// Cert statuses.
const (
	CertPending = "pending"
	CertOK      = "ok"
	CertError   = "error"
	CertOff     = "off"
)

type Deployment struct {
	ID       string `gorm:"primaryKey"`
	RelayID  string
	DomainID *string
	// Subdomain is the label under a wildcard domain; "" on an exact one.
	Subdomain string
	// Host is the full public hostname (unique). nil only for rows from
	// before domains, until Seed fills it in.
	Host           *string
	Protocol       string // http | https
	TargetHost     string
	TargetPort     int
	TLSSkipVerify  bool   `gorm:"column:tls_skip_verify"`
	SNI            string `gorm:"column:sni"`
	PreserveHost   bool
	Enabled        bool
	AuthMode       string
	LoginPassHash  string
	AllowRootLogin bool
	SessionTTLS    int `gorm:"column:session_ttl_s"`
	BasicUser      string
	BasicPassHash  string
	// IPAllowlist is newline-separated CIDRs; empty allows everyone.
	IPAllowlist   string `gorm:"column:ip_allowlist"`
	CertStatus    string
	CertExpiresAt *time.Time
	CertError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type DeploymentToken struct {
	ID           string `gorm:"primaryKey"`
	DeploymentID string
	Name         string
	TokenHash    string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

type AdminSession struct {
	TokenHash  string `gorm:"primaryKey"`
	PasswordFP string `gorm:"column:password_fp"`
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UserAgent  string
}

// First returns the first match of q, or nil if there is none.
func First[T any](q *gorm.DB) (*T, error) {
	var v T
	err := q.First(&v).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// NewID is a short random row ID.
func NewID() string { return lower(randomString(10)) }

// NewSecret is a bearer secret with a recognizable prefix, e.g. "smr_…".
func NewSecret(prefix string) string { return prefix + "_" + lower(randomString(25)) }

// HashSecret is how bearer secrets are stored and looked up. They're 125
// random bits, so a plain hash is enough (no slow KDF needed).
func HashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Domain is a name deployments can live on: a wildcard domain
// (*.example.com, Name "example.com") gives each deployment a subdomain,
// an exact one ("status.foo.org") is one deployment's whole hostname.
type Domain struct {
	ID        string `gorm:"primaryKey"`
	Name      string
	Wildcard  bool
	CreatedAt time.Time
}

// Label is how a domain is written: "*.example.com" or "status.foo.org".
func (d Domain) Label() string {
	if d.Wildcard {
		return "*." + d.Name
	}
	return d.Name
}

// HostFor is the hostname a deployment with subdomain sub gets on d.
func (d Domain) HostFor(sub string) string {
	if d.Wildcard {
		return sub + "." + d.Name
	}
	return d.Name
}
