package registry

import (
	"net/netip"
	"testing"
)

func TestAllowlist(t *testing.T) {
	nets, err := ParseAllowlist("10.0.0.0/8, 192.168.1.5\n2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{Nets: nets}
	for ip, want := range map[string]bool{
		"10.1.2.3":        true,
		"192.168.1.5":     true,
		"192.168.1.6":     false,
		"::ffff:10.0.0.1": true,
		"2001:db8::1":     true,
		"8.8.8.8":         false,
		"garbage":         false,
	} {
		if got := e.Allowed(ip); got != want {
			t.Errorf("%s: %v", ip, got)
		}
	}
	if (&Entry{}).Allowed("8.8.8.8") != true {
		t.Error("empty allowlist should allow everyone")
	}
	if (&Entry{Restricted: true}).Allowed("8.8.8.8") {
		t.Error("an allow list of empty IP groups should let nobody in")
	}
	if (&BridgeEntry{Restricted: true}).Allowed(netip.MustParseAddr("8.8.8.8")) {
		t.Error("a bridge allow list of empty IP groups should let nobody in")
	}
	if _, err := ParseAllowlist("10.0.0.0/33"); err == nil {
		t.Error("bad prefix accepted")
	}
}

func TestPathAllowed(t *testing.T) {
	nets, err := ParseAllowlist("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{PathRules: []PathRuleEntry{
		{PathPrefix: "/secret", Nets: nets},
	}}
	for path, want := range map[string]bool{
		"/secret":   false, // outside 10.0.0.0/8
		"/secret/x": false,
		"/secretly": true, // not under /secret: no rule applies
		"/other":    true,
		"/":         true,
	} {
		if got := e.PathAllowed(path, "203.0.113.1"); got != want {
			t.Errorf("%s: PathAllowed(outside) = %v, want %v", path, got, want)
		}
		if got := e.PathAllowed(path, "10.1.2.3"); !got {
			t.Errorf("%s: PathAllowed(inside 10.0.0.0/8) should always be true", path)
		}
	}
	if (&Entry{}).PathAllowed("/secret", "8.8.8.8") != true {
		t.Error("no path rules should mean no extra restriction")
	}

	// The most specific (longest) matching prefix wins.
	e = &Entry{PathRules: []PathRuleEntry{
		{PathPrefix: "/a/b", Nets: nil}, // no restriction
		{PathPrefix: "/a", Nets: nets},  // restricted
	}}
	if !e.PathAllowed("/a/b", "203.0.113.1") {
		t.Error("the longer, unrestricted prefix should win over the shorter, restricted one")
	}
	if e.PathAllowed("/a/x", "203.0.113.1") {
		t.Error("/a/x only matches the restricted /a rule")
	}
}

func TestAllowsEmail(t *testing.T) {
	e := &Entry{OTPEmails: map[string]bool{"ann@example.com": true, "*@corp.com": true}}
	for email, want := range map[string]bool{
		"ann@example.com":   true,
		"bob@example.com":   false,
		"bob@corp.com":      true,
		"bob@sub.corp.com":  false,
		"bob@corp.com.evil": false,
		"*@corp.com":        false,
		"corp.com":          false,
	} {
		if got := e.AllowsEmail(email); got != want {
			t.Errorf("%s: %v", email, got)
		}
	}
}
