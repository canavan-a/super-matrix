// Package schema is the declarative schema document: a YAML (or JSON)
// description of a server's domains, groups, deployments and bridges that
// the server can plan and apply in one go, the way CI applies
// infrastructure as code. This package only reads, checks and writes
// documents; the server (package api) diffs one against its database.
//
// Everything a document lists is the whole truth by default ("strict"):
// a deployment it lists has exactly the values given, its missing fields
// take their defaults, and a collection it lists loses whatever it
// doesn't. Three markers hand things back to the app:
//
//   - "..." as an entry of a list (ip_allowlist: [10.0.0.0/8, "..."]):
//     the schema's entries are kept, and the app may add more.
//   - "...": true as a key of a map (deployments, path_rules, groups.ip …):
//     the schema's items are kept, and the app may add more.
//   - ui: [field, …] on an item: those fields are the app's. A value given
//     for one is only used when the item is created.
//
// A collection left out of the document is left alone entirely.
package schema

import (
	"reflect"
	"strconv"
	"strings"
)

// Document versions this build reads. A document at an older version is
// converted (convert.go) before it's used; a newer one is refused.
const (
	MinVersion     = 1
	CurrentVersion = 1
)

// Spread is the marker that hands the rest of a list or map to the app.
const Spread = "..."

// Document is one schema document.
type Document struct {
	// Version is the document format's version (see CurrentVersion).
	Version int `json:"version" desc:"Document format version."`
	// From is the version the document was written at, before Parse
	// upgraded it: fields newer than that are left to the app.
	From int `json:"-"`
	// Domains are labels: "*.example.com" (a wildcard: deployments get a
	// name under it) or "status.example.com" (exactly one name).
	Domains     *List            `json:"domains,omitempty" desc:"Domains, as *.example.com (wildcard) or status.example.com (exact). \"...\" keeps domains added in the app."`
	Groups      *Groups          `json:"groups,omitempty" desc:"Named lists that allow lists and sign-in lists include by name."`
	Deployments *Map[Deployment] `json:"deployments,omitempty" desc:"Deployments by hostname. \"...\": true keeps deployments added in the app."`
	Bridges     *Map[Bridge]     `json:"bridges,omitempty" desc:"Bridges by slug. \"...\": true keeps bridges added in the app."`
}

// Groups are the named lists.
type Groups struct {
	IP    *Map[Group] `json:"ip,omitempty" desc:"IP groups by name: addresses and CIDR ranges."`
	Email *Map[Group] `json:"email,omitempty" desc:"Email groups by name: addresses and *@domain wildcards."`
}

// Group is an IP or email group.
type Group struct {
	Entries *List    `json:"entries,omitempty" since:"1" desc:"The group's entries."`
	UI      []string `json:"ui,omitempty" desc:"Fields the app manages."`
}

// Deployment is one deployment, keyed by its hostname.
type Deployment struct {
	// Relay is the relay's slug.
	Relay *string `json:"relay,omitempty" since:"1" desc:"Slug of the relay that reaches the target."`
	// Domain picks the domain when the hostname alone doesn't (an exact
	// domain and a wildcard can both fit). It isn't a managed value.
	Domain        *string `json:"domain,omitempty" key:"true" desc:"The domain the hostname is on, e.g. *.example.com; only needed when more than one fits."`
	Protocol      *string `json:"protocol,omitempty" since:"1" enum:"http,https,ssh" default:"http"`
	TargetHost    *string `json:"target_host,omitempty" since:"1" desc:"Hostname or IP address the relay connects to."`
	TargetPort    *int    `json:"target_port,omitempty" since:"1" min:"1" max:"65535" desc:"Port the relay connects to (22 by default for ssh)."`
	TLSSkipVerify *bool   `json:"tls_skip_verify,omitempty" since:"1" default:"false"`
	SNI           *string `json:"sni,omitempty" since:"1" default:""`
	PreserveHost  *bool   `json:"preserve_host,omitempty" since:"1" default:"false"`
	Enabled       *bool   `json:"enabled,omitempty" since:"1" default:"true"`
	AuthMode      *string `json:"auth_mode,omitempty" since:"1" enum:"none,login,otp,basic,token" default:"login"`
	// LoginPassword and BasicPassword are bcrypt hashes; left out, the app
	// manages them, so no password has to be written down.
	LoginPassword     *Hash   `json:"login_password,omitempty" since:"1" desc:"The login password as a bcrypt hash. Left out, the app sets it."`
	AllowRootLogin    *bool   `json:"allow_root_login,omitempty" since:"1" default:"true"`
	SessionTTLS       *int    `json:"session_ttl_s,omitempty" since:"1" min:"300" default:"604800"`
	BasicUser         *string `json:"basic_user,omitempty" since:"1" default:""`
	BasicPassword     *Hash   `json:"basic_password,omitempty" since:"1" desc:"The basic auth password as a bcrypt hash. Left out, the app sets it."`
	OTPEmails         *List   `json:"otp_emails,omitempty" since:"1" desc:"Who can get an email code: addresses and *@domain wildcards."`
	EmailGroups       *List   `json:"email_groups,omitempty" since:"1" desc:"Email groups (by name) whose people can get an email code."`
	IPAllowlist       *List   `json:"ip_allowlist,omitempty" since:"1" desc:"Addresses and CIDR ranges allowed in; empty allows everyone."`
	IPGroups          *List   `json:"ip_groups,omitempty" since:"1" desc:"IP groups (by name) allowed in too."`
	SSHCLI            *bool   `json:"ssh_cli,omitempty" since:"1" default:"true"`
	SSHShell          *bool   `json:"ssh_shell,omitempty" since:"1" default:"true"`
	SSHUser           *string `json:"ssh_user,omitempty" since:"1" default:""`
	RateLimitEnabled  *bool   `json:"rate_limit_enabled,omitempty" since:"1" default:"false"`
	RateLimitRequests *int    `json:"rate_limit_requests,omitempty" since:"1" min:"1" default:"100"`
	RateLimitPeriodS  *int    `json:"rate_limit_period_s,omitempty" since:"1" enum:"1,60" default:"60"`
	RateLimitBurst    *int    `json:"rate_limit_burst,omitempty" since:"1" min:"0" default:"0"`
	// PathRules are extra allow lists by path prefix.
	PathRules *Map[PathRule] `json:"path_rules,omitempty" since:"1" desc:"Extra IP allow lists by path prefix. \"...\": true keeps rules added in the app."`
	UI        []string       `json:"ui,omitempty" desc:"Fields the app manages; a value given for one is only used on create."`
}

// PathRule is an extra allow list on a path prefix, keyed by the prefix.
type PathRule struct {
	IPAllowlist *List    `json:"ip_allowlist,omitempty" since:"1" desc:"Addresses and CIDR ranges allowed to this path."`
	UI          []string `json:"ui,omitempty" desc:"Fields the app manages."`
}

// Bridge is one TCP or UDP bridge, keyed by its slug.
type Bridge struct {
	Name     *string `json:"name,omitempty" since:"1" desc:"Display name; the slug by default."`
	Protocol *string `json:"protocol,omitempty" since:"1" enum:"tcp,udp" default:"tcp"`
	Enabled  *bool   `json:"enabled,omitempty" since:"1" default:"true"`
	// Public bridges listen on the server; others on FromRelay's machine.
	Public     *bool   `json:"public,omitempty" since:"1" default:"false" desc:"Listen on the server itself instead of a relay."`
	FromRelay  *string `json:"from_relay,omitempty" since:"1" desc:"Slug of the relay that listens (not for public bridges)."`
	ListenHost *string `json:"listen_host,omitempty" since:"1" desc:"Address to listen on, on the from relay (127.0.0.1 by default)."`
	ListenPort *int    `json:"listen_port,omitempty" since:"1" min:"1" max:"65535"`
	ToRelay    *string `json:"to_relay,omitempty" since:"1" desc:"Slug of the relay that reaches the target."`
	TargetHost *string `json:"target_host,omitempty" since:"1"`
	TargetPort *int    `json:"target_port,omitempty" since:"1" min:"1" max:"65535" desc:"The listen port by default."`
	UDPIdleS   *int    `json:"udp_idle_s,omitempty" since:"1" min:"30" max:"86400"`
	// IPAllowlist and IPGroups are for public bridges.
	IPAllowlist *List `json:"ip_allowlist,omitempty" since:"1" desc:"Public bridges: addresses and CIDR ranges allowed in."`
	IPGroups    *List `json:"ip_groups,omitempty" since:"1" desc:"Public bridges: IP groups (by name) allowed in too."`
	// Domain and Subdomain give a public bridge a DNS name.
	Domain    *string  `json:"domain,omitempty" since:"1" desc:"Public bridges: a domain for its DNS name, e.g. *.example.com."`
	Subdomain *string  `json:"subdomain,omitempty" since:"1" desc:"Public bridges: the name under a wildcard domain."`
	UI        []string `json:"ui,omitempty" desc:"Fields the app manages; a value given for one is only used on create."`
}

// Hash is a password as a bcrypt hash, never in plain text.
type Hash struct {
	Hash string `json:"hash"`
}

// Fields is the managed fields of an item type (a pointer to Deployment,
// Bridge, Group or PathRule): every field but ui and those that only pick
// the item (key:"true").
func Fields(item any) []string {
	t := reflect.TypeOf(item)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := jsonName(f)
		if name == "" || name == "ui" || f.Tag.Get("key") == "true" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Since is the document version that introduced field of item's type; a
// document older than that doesn't know the field, so the app keeps
// managing it.
func Since(item any, field string) int {
	t := reflect.TypeOf(item)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if jsonName(f) == field {
			if n, err := strconv.Atoi(f.Tag.Get("since")); err == nil {
				return n
			}
		}
	}
	return MinVersion
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}
