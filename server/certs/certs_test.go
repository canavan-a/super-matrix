package certs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"main/config"
)

func TestHostPolicy(t *testing.T) {
	cfg := config.Config{AdminHost: "matrix.example.com", BaseDomain: "example.com", CertDir: t.TempDir()}
	m := New(cfg, func(h string) bool { return h == "app.example.com" })
	for host, ok := range map[string]bool{
		"matrix.example.com": true,
		"app.example.com":    true,
		"other.example.com":  false,
		"evil.test":          false,
	} {
		if err := m.policy(context.Background(), host); (err == nil) != ok {
			t.Errorf("%s: err=%v", host, err)
		}
	}

	// :80 redirects known hosts to https and 404s the rest.
	h := m.HTTPHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example.com/x?y=1", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://app.example.com/x?y=1" {
		t.Fatalf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://evil.test/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", rec.Code)
	}
}
