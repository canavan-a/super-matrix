package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type resolved struct {
	NixServices map[string]struct {
		Flake     string            `yaml:"flake"`
		FlakeLock string            `yaml:"flake_lock"`
		Files     map[string]string `yaml:"files"`
		Source    string            `yaml:"source"`
		Relays    map[string]any    `yaml:"relays"`
	} `yaml:"nix_services"`
}

func TestResolveNixFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"nix/kuma/flake.nix":          "{ outputs = _: {}; }",
		"nix/kuma/flake.lock":         `{"version":7}`,
		"nix/kuma/config/app.json":    "{}",
		"nix/kuma/scratch.tmp":        "x",
		"nix/kuma/.supermatrixignore": "*.tmp\n",
		"nix/kuma/.git/HEAD":          "ref",
		"nix/agent/flake.nix":         "{ agent }",
		"secret.nix":                  "outside",
	})
	_ = os.Symlink("/nix/store/x", filepath.Join(root, "nix/kuma/result"))
	doc := `version: 1
nix_services:
  kuma:
    source: ./nix/kuma
    relays: {homelab: {}}
  agent:
    flake: {file: nix/agent/flake.nix}
    files:
      extra.txt: {file: ./nix/kuma/config/app.json}
`
	out, changed, err := resolveNixFiles([]byte(doc), root, false)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	var r resolved
	if err := yaml.Unmarshal(out, &r); err != nil {
		t.Fatal(err, string(out))
	}
	k := r.NixServices["kuma"]
	if k.Flake != "{ outputs = _: {}; }" || k.FlakeLock != `{"version":7}` || k.Source != "" || k.Relays["homelab"] == nil {
		t.Fatalf("kuma: %+v", k)
	}
	if len(k.Files) != 1 || k.Files["config/app.json"] != "{}" {
		t.Fatalf("kuma files: %v", k.Files)
	}
	a := r.NixServices["agent"]
	if a.Flake != "{ agent }" || a.Files["extra.txt"] != "{}" {
		t.Fatalf("agent: %+v", a)
	}

	// Outside the document's directory only when allowed.
	sub := filepath.Join(root, "nix")
	outside := "version: 1\nnix_services:\n  x:\n    flake: {file: ../secret.nix}\n"
	if _, _, err := resolveNixFiles([]byte(outside), sub, false); err == nil || !strings.Contains(err.Error(), "-allow-outside") {
		t.Fatalf("outside: %v", err)
	}
	if out, _, err := resolveNixFiles([]byte(outside), sub, true); err != nil || !strings.Contains(string(out), "outside") {
		t.Fatalf("allowed outside: %v %s", err, out)
	}

	// A document without references goes as it is, comments and all.
	plain := "version: 1 # keep me\ndomains: [\"*.example.com\"]\n"
	if out, changed, _ := resolveNixFiles([]byte(plain), root, false); changed || string(out) != plain {
		t.Fatalf("plain: %v %q", changed, out)
	}

	bad := "version: 1\nnix_services:\n  x:\n    source: ./nix/kuma\n    flake: inline\n"
	if _, _, err := resolveNixFiles([]byte(bad), root, false); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both: %v", err)
	}
	if _, _, err := resolveNixFiles([]byte("version: 1\nnix_services:\n  x:\n    source: ./nope\n"), root, false); err == nil {
		t.Fatal("a missing source")
	}
}

func TestSplitExport(t *testing.T) {
	root := t.TempDir()
	js := `{"version":1,"nix_services":{"kuma":{"run":"package","flake":"{ kuma }","flake_lock":"{\"version\":7}","files":{"config/app.json":"{}"},"relays":{"homelab":{}}}}}`
	out, err := splitExport([]byte(js), filepath.Join(root, "nix"), root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "source: ./nix/kuma") || strings.Contains(string(out), "{ kuma }") {
		t.Fatalf("split doc:\n%s", out)
	}
	for p, want := range map[string]string{"nix/kuma/flake.nix": "{ kuma }", "nix/kuma/flake.lock": `{"version":7}`, "nix/kuma/config/app.json": "{}"} {
		if b, err := os.ReadFile(filepath.Join(root, p)); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", p, b, err)
		}
	}
	// And reading it back gives the same service.
	back, _, err := resolveNixFiles(out, root, false)
	if err != nil {
		t.Fatal(err)
	}
	var r resolved
	_ = yaml.Unmarshal(back, &r)
	if k := r.NixServices["kuma"]; k.Flake != "{ kuma }" || k.FlakeLock != `{"version":7}` || k.Files["config/app.json"] != "{}" {
		t.Fatalf("round trip: %+v", k)
	}
}
