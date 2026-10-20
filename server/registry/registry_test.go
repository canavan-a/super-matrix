package registry

import "testing"

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
	if _, err := ParseAllowlist("10.0.0.0/33"); err == nil {
		t.Error("bad prefix accepted")
	}
}
