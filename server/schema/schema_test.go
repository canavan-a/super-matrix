package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFixturesParse(t *testing.T) {
	files, _ := filepath.Glob("testdata/v*/*.yaml")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestParseMarkers(t *testing.T) {
	data, _ := os.ReadFile("testdata/v1/full.yaml")
	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.From != 1 || !d.Domains.Spread || !slices.Equal(d.Domains.Items, []string{"*.example.com", "status.example.org"}) {
		t.Fatalf("domains: %+v", d.Domains)
	}
	if !d.Deployments.Spread || !slices.Equal(d.Deployments.Keys, []string{"grafana.example.com", "status.example.org"}) {
		t.Fatalf("deployments: %+v", d.Deployments)
	}
	g := d.Deployments.Get("grafana.example.com")
	if *g.TargetPort != 3000 || !g.IPAllowlist.Spread || len(g.IPAllowlist.Items) != 0 || g.PathRules.Spread || g.PathRules.Get("/admin") == nil {
		t.Fatalf("grafana: %+v", g)
	}
	if !slices.Equal(g.UI, []string{"enabled"}) || g.Enabled != nil {
		t.Fatalf("ui: %+v", g.UI)
	}
	if d.Groups.Email.Get("team").Entries.Spread || !d.Groups.IP.Get("office").Entries.Spread {
		t.Fatal("group spreads")
	}
	// Round trip through YAML keeps everything.
	y, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Parse(y)
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	a, _ := json.Marshal(d)
	b, _ := json.Marshal(d2)
	if string(a) != string(b) {
		t.Fatalf("round trip:\n%s\n%s", a, b)
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{"", "empty"},
		{"deployments: {}", "version: missing"},
		{"version: 99", "newer than this server"},
		{"version: 0.5", "version: expected a whole number"},
		{"version: 1\nfoo: 1", "foo: unknown field"},
		{"version: 1\ndeployments:\n  a.example.com:\n    targt_host: x", "deployments.a.example.com.targt_host: unknown field"},
		{"version: 1\ndeployments:\n  a.example.com:\n    target_port: http", "deployments.a.example.com.target_port: expected a whole number"},
		{"version: 1\ndeployments:\n  a.example.com:\n    login_password: hunter22", "never the password itself"},
		{"version: 1\ndeployments:\n  a.example.com:\n    login_password: {hash: nope}", "not a bcrypt hash"},
		{"version: 1\ndeployments:\n  a.example.com:\n    ui: [colour]", `deployments.a.example.com.ui: "colour" isn't a field`},
		{"version: 1\ndeployments:\n  \"...\": yes please", "spread marker"},
		{"version: 1\ndomains: [1]", "domains[0]: expected a string"},
		{"version: 1\ngroups:\n  ip:\n    o:\n      entries: [1.2.3.4]\n  mail: {}", "groups.mail: unknown field"},
		{"[1, 2]", "must be a map"},
		{"version: 1\ngroups:\n  ip:\n    Office: {entries: [10.0.0.0/8]}\n    office: {entries: [1.2.3.4]}", `groups.ip.office: the same name as "Office"`},
		{"version: 1\ndeployments:\n  App.example.com: {}\n  app.example.com: {}", `deployments.app.example.com: the same name as "App.example.com"`},
	} {
		_, err := Parse([]byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.doc, err, c.want)
		}
	}
}

// A collection given as null (an empty YAML value) is an empty collection,
// not a missing one: "deployments:" with nothing under it means none.
func TestNullCollectionIsEmpty(t *testing.T) {
	d, err := Parse([]byte("version: 1\ndeployments:\ndomains:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Deployments == nil || len(d.Deployments.Keys) != 0 || d.Domains == nil {
		t.Fatalf("%+v", d)
	}
}

func TestJSONInput(t *testing.T) {
	d, err := Parse([]byte(`{"version":1,"bridges":{"ssh":{"from_relay":"a","to_relay":"b","listen_port":2222,"target_host":"10.0.0.1","target_port":22}}}`))
	if err != nil || *d.Bridges.Get("ssh").ListenPort != 2222 {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestJSONSchema(t *testing.T) {
	b, err := json.Marshal(JSONSchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"maximum":65535`, `"...":{"description"`, `"required":["hash"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestFields(t *testing.T) {
	f := Fields(&Deployment{})
	if slices.Contains(f, "ui") || slices.Contains(f, "domain") || !slices.Contains(f, "path_rules") || !slices.Contains(f, "target_host") {
		t.Fatal(f)
	}
	if Since(&Deployment{}, "target_host") != 1 {
		t.Fatal("since")
	}
}
