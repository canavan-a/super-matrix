package database

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Relay struct {
	ID   string `gorm:"primaryKey"`
	Name string
	// Slug is the relay's friendly ID (see ValidSlug), unique among relays;
	// config files name the relay by it.
	Slug       string
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
	// ManageDeployments lets the relay create, read, update and delete its
	// own deployments with its token (/v1/relay/*); off by default.
	ManageDeployments bool
	// Features is what the relay said it supports in its last hello,
	// comma-separated (e.g. "bridge-tcp,bridge-udp").
	Features string
}

// HasFeature reports whether the relay's last hello listed f.
func (r Relay) HasFeature(f string) bool {
	for _, x := range strings.Split(r.Features, ",") {
		if x == f {
			return true
		}
	}
	return false
}

// Bridge protocols.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// Bridge is a TCP or UDP port that leads to TargetHost:TargetPort on
// ToRelayID's network. It listens on FromRelayID's machine, or on the
// server itself (a public bridge) when FromRelayID is nil.
type Bridge struct {
	ID   string `gorm:"primaryKey"`
	Name string
	// Slug is the bridge's friendly ID (see ValidSlug), unique among
	// bridges.
	Slug        string
	Protocol    string // tcp | udp
	Enabled     bool
	FromRelayID *string
	ListenHost  string
	ListenPort  int
	ToRelayID   string
	TargetHost  string
	TargetPort  int
	// UDPIdleS is how long a UDP sender may stay quiet before its flow is
	// forgotten.
	UDPIdleS int `gorm:"column:udp_idle_s"`
	// IPAllowlist is newline-separated CIDRs for public bridges; empty
	// allows everyone.
	IPAllowlist string `gorm:"column:ip_allowlist"`
	// DomainID, Subdomain and Host give a public bridge a DNS name (Host,
	// "" when it has none): its address is Host:ListenPort. Routing is by
	// port; the name only has to resolve to the server.
	DomainID  *string
	Subdomain string
	Host      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Public reports whether the bridge listens on the server.
func (b Bridge) Public() bool { return b.FromRelayID == nil }

// Auth modes a deployment can sit behind.
const (
	AuthNone  = "none"
	AuthLogin = "login"
	AuthBasic = "basic"
	AuthToken = "token"
	// AuthOTP emails a one-time code to addresses on the deployment's list.
	AuthOTP = "otp"
)

// Protocols a deployment's target can speak. http and https are proxied
// as HTTP; ssh is reached through the CLI proxy or the browser shell.
const (
	ProtocolHTTP  = "http"
	ProtocolHTTPS = "https"
	ProtocolSSH   = "ssh"
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
	Protocol       string // http | https | ssh
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
	// OTPEmails is the newline-separated, lowercased addresses that can get
	// a sign-in code in otp mode.
	OTPEmails string `gorm:"column:otp_emails"`
	// IPAllowlist is newline-separated CIDRs; empty allows everyone.
	IPAllowlist string `gorm:"column:ip_allowlist"`
	// SSHCLI and SSHShell turn on the CLI proxy and the browser shell of an
	// ssh deployment; SSHUser is the username the shell suggests.
	SSHCLI   bool   `gorm:"column:ssh_cli"`
	SSHShell bool   `gorm:"column:ssh_shell"`
	SSHUser  string `gorm:"column:ssh_user"`
	// SSHHostKey is the target's host key fingerprint (SHA256:…), saved the
	// first time the shell connects; "" until then.
	SSHHostKey string `gorm:"column:ssh_host_key"`
	// RateLimitEnabled caps requests from a single client IP to this
	// deployment at the edge. RateLimitPeriodS is the sustained window's
	// length in seconds (60 = "per minute", 1 = "per second");
	// RateLimitBurst, if > 0, additionally caps a short 1s window.
	RateLimitEnabled  bool `gorm:"column:rate_limit_enabled"`
	RateLimitRequests int  `gorm:"column:rate_limit_requests"`
	RateLimitPeriodS  int  `gorm:"column:rate_limit_period_s"`
	RateLimitBurst    int  `gorm:"column:rate_limit_burst"`
	CertStatus        string
	CertExpiresAt     *time.Time
	CertError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DeploymentToken struct {
	ID           string `gorm:"primaryKey"`
	DeploymentID string
	Name         string
	TokenHash    string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
	// ExpiresAt is when the token stops working; nil never.
	ExpiresAt *time.Time
}

// PathRule layers an extra IP allowlist onto one path prefix (and
// everything under it) of a Deployment, checked at the edge in addition
// to (never instead of) the deployment's own IPAllowlist. IPAllowlist
// here is empty by default, which adds no further restriction.
type PathRule struct {
	ID           string `gorm:"primaryKey"`
	DeploymentID string
	// PathPrefix is matched against the request path: the path itself, or
	// anything under it ("/secret" matches "/secret" and "/secret/x", not
	// "/secretly"). Always starts with "/".
	PathPrefix string `gorm:"column:path_prefix"`
	// IPAllowlist is newline-separated CIDRs/addresses, the same format as
	// Deployment.IPAllowlist; empty adds no further restriction.
	IPAllowlist string `gorm:"column:ip_allowlist"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
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

// IPGroup is a named set of addresses and CIDR ranges that allow lists
// can include by reference.
type IPGroup struct {
	ID   string `gorm:"primaryKey"`
	Name string
	// Entries is newline-separated CIDRs, as in IPAllowlist.
	Entries   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (IPGroup) TableName() string { return "ip_groups" }

// DeploymentIPGroup includes an IP group in a deployment's allow list.
type DeploymentIPGroup struct {
	DeploymentID string `gorm:"primaryKey"`
	IPGroupID    string `gorm:"primaryKey;column:ip_group_id"`
}

func (DeploymentIPGroup) TableName() string { return "deployment_ip_groups" }

// BridgeIPGroup includes an IP group in a public bridge's allow list.
type BridgeIPGroup struct {
	BridgeID  string `gorm:"primaryKey"`
	IPGroupID string `gorm:"primaryKey;column:ip_group_id"`
}

func (BridgeIPGroup) TableName() string { return "bridge_ip_groups" }

// EmailGroup is a named list of email addresses (and *@domain wildcards)
// that email-code sign-in lists can include by reference.
type EmailGroup struct {
	ID   string `gorm:"primaryKey"`
	Name string
	// Entries is newline-separated normalized addresses, as in OTPEmails.
	Entries   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (EmailGroup) TableName() string { return "email_groups" }

// DeploymentEmailGroup includes an email group in a deployment's
// email-code sign-in list.
type DeploymentEmailGroup struct {
	DeploymentID string `gorm:"primaryKey"`
	EmailGroupID string `gorm:"primaryKey"`
}

func (DeploymentEmailGroup) TableName() string { return "deployment_email_groups" }
