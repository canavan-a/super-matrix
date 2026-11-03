package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestLocalAPI(t *testing.T) {
	var gotPath, gotAuth, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCookie = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	h, err := localAPI(up.URL, "smr_secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host, path, origin string
		want               int
		upstream           string
	}{
		{"127.0.0.1:3388", "/deployments", "", 200, "/v1/relay/deployments"},
		{"localhost:3388", "/deployments/abc/tokens", "", 200, "/v1/relay/deployments/abc/tokens"},
		{"[::1]:3388", "/me", "", 200, "/v1/relay/me"},
		// A web page on this machine.
		{"127.0.0.1:3388", "/deployments", "https://evil.example", 403, ""},
		// DNS rebinding.
		{"evil.example:3388", "/deployments", "", 403, ""},
		// Only the relay's own API passes.
		{"127.0.0.1:3388", "/relays", "", 404, ""},
		{"127.0.0.1:3388", "/deploymentsx", "", 404, ""},
		{"127.0.0.1:3388", "/deployments/../../relays", "", 404, ""},
	} {
		gotPath, gotAuth = "", ""
		req := httptest.NewRequest("GET", "http://"+c.host+c.path, nil)
		req.Host = c.host
		req.URL.Path = c.path
		req.Header.Set("Cookie", "x=y")
		req.Header.Set("Authorization", "Bearer smr_other")
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d %s", c.host, c.path, rec.Code, body)
			continue
		}
		if gotPath != c.upstream {
			t.Errorf("%s %s: upstream got %q", c.host, c.path, gotPath)
		}
		if c.upstream != "" && (gotAuth != "Bearer smr_secret" || gotCookie != "") {
			t.Errorf("%s: auth %q cookie %q", c.path, gotAuth, gotCookie)
		}
	}
}

func TestServeLocalOnlyLoopback(t *testing.T) {
	if err := serveLocal(t.Context(), "0.0.0.0:0", "http://x", "t"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatal(err)
	}
}

func TestParseTargetURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:3000": "http 127.0.0.1 3000",
		"127.0.0.1:3000":        "http 127.0.0.1 3000",
		"https://nas.lan":       "https nas.lan 443",
		"ssh://box":             "ssh box 22",
		"ssh://[::1]:2222":      "ssh ::1 2222",
		"HTTPS://x:8443/":       "https x 8443",
		"ftp://x":               "error",
		"http://x/path":         "error",
		"http://x:abc":          "error",
	} {
		p, h, port, err := parseTargetURL(in)
		got := "error"
		if err == nil {
			got = strings.Join([]string{p, h, strconv.Itoa(port)}, " ")
		}
		if got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestPlaceHost(t *testing.T) {
	ds := []domain{
		{ID: "w", Name: "example.com", Wildcard: true, Label: "*.example.com"},
		{ID: "a", Name: "example.com", Label: "example.com"},
		{ID: "s", Name: "status.foo.org", Label: "status.foo.org"},
	}
	for in, want := range map[string]string{
		"grafana.example.com": "w grafana",
		"example.com":         "a ",
		"Status.Foo.Org.":     "s ",
		"x.status.foo.org":    "error",
		"other.net":           "error",
	} {
		id, sub, err := placeHost(in, ds)
		got := "error"
		if err == nil {
			got = id + " " + sub
		}
		if got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}
