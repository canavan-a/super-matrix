package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNixCommands(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer smr_test" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unknown relay token"}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/relay/nix-services":
			_ = json.Unmarshal(body, &created)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "GET" && r.URL.Path == "/v1/relay/nix-services/kuma":
			_, _ = w.Write([]byte(`{"slug":"kuma","applied":[{"id":"old","removing":true},{"id":"a1","phase":"running"}]}`))
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte("journal text\n"))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	_ = os.WriteFile(tokFile, []byte("smr_test\n"), 0o600)
	flake := filepath.Join(dir, "flake")
	_ = os.MkdirAll(filepath.Join(flake, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(flake, "flake.nix"), []byte("{ outputs = _: {}; }"), 0o644)
	_ = os.WriteFile(filepath.Join(flake, "flake.lock"), []byte(`{"version":7}`), 0o644)
	_ = os.WriteFile(filepath.Join(flake, ".git", "HEAD"), []byte("x"), 0o644)
	t.Setenv("SUPERMATRIX_RELAY_ENV", filepath.Join(dir, "none.env"))
	t.Setenv("SUPERMATRIX_SERVER_URL", srv.URL)
	t.Setenv("SUPERMATRIX_RELAY_TOKEN_FILE", tokFile)

	if err := cmdNix([]string{"create", "kuma", "--dir", flake, "--env", "PORT=3001", "--relay-env", "DB_PASSWORD", "--command", "kuma --port 3001", "--apply"}); err != nil {
		t.Fatal(err)
	}
	files, _ := created["files"].(map[string]any)
	if created["slug"] != "kuma" || files["flake.nix"] == nil || files["flake.lock"] == nil || files[".git/HEAD"] != nil {
		t.Fatalf("created: %v", created)
	}
	if env, _ := created["env"].(map[string]any); env["PORT"] != "3001" {
		t.Fatalf("env: %v", created["env"])
	}
	if cmd, _ := created["command"].([]any); len(cmd) != 3 || cmd[0] != "kuma" {
		t.Fatalf("command: %v", created["command"])
	}
	for _, c := range [][]string{{"stop", "kuma"}, {"rebuild", "kuma"}, {"remove", "kuma"}, {"logs", "kuma", "--lines", "50"}} {
		if err := cmdNix(c); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	want := []string{
		"POST /v1/relay/nix-services",
		"POST /v1/relay/nix-services/kuma/applied",
		"GET /v1/relay/nix-services/kuma", "PATCH /v1/relay/applied-nix-services/a1",
		"GET /v1/relay/nix-services/kuma", "POST /v1/relay/applied-nix-services/a1/rebuild",
		"GET /v1/relay/nix-services/kuma", "DELETE /v1/relay/applied-nix-services/a1",
		"GET /v1/relay/nix-services/kuma", "GET /v1/relay/applied-nix-services/a1/logs?lines=50",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}

	// Without root (the token file unreadable), it says so.
	_ = os.Chmod(tokFile, 0)
	if os.Geteuid() != 0 {
		if err := cmdNix([]string{"list"}); err == nil || !strings.Contains(err.Error(), "run this as root") {
			t.Fatalf("unreadable token: %v", err)
		}
	}
}
