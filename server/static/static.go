// Package static serves the app's built web client (`npm run build:web`, a
// Vite/react-native-web static bundle with nothing secret in it) on the
// admin host, next to the API.
package static

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	// Not in every system's MIME table; browsers want this exact type.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// Handler serves dir as static files with SPA fallback: a request for a path
// that doesn't exist on disk is served dir/index.html instead of 404ing, so
// react-native-web's client-side routing survives a refresh or deep link.
// The web app is installable (a PWA, see
// app/pwa): its service worker, the page and the manifest must always be
// revalidated so updates are found, while Vite's content-hashed assets
// never change and can be cached for good.
func Handler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case r.URL.Path == "/sw.js":
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Service-Worker-Allowed", "/")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
