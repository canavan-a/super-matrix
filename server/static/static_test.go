package static

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheHeaders(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html":           "<html>",
		"sw.js":                "// sw",
		"manifest.webmanifest": "{}",
		"assets/index-abc.js":  "js",
	} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(dir)
	for _, c := range []struct{ path, cache, ctype string }{
		{"/", "no-cache", "text/html; charset=utf-8"},
		{"/room/deep/link", "no-cache", "text/html; charset=utf-8"},
		{"/sw.js", "no-cache", ""},
		{"/manifest.webmanifest", "no-cache", "application/manifest+json"},
		{"/assets/index-abc.js", "public, max-age=31536000, immutable", ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: %d cache %q", c.path, rec.Code, rec.Header().Get("Cache-Control"))
		}
		if c.ctype != "" && rec.Header().Get("Content-Type") != c.ctype {
			t.Errorf("%s: content type %q", c.path, rec.Header().Get("Content-Type"))
		}
	}
}
