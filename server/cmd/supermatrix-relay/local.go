package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// DefaultLocalAddr is where the relay serves its local API, and where the
// CLI looks for it.
const DefaultLocalAddr = "127.0.0.1:3388"

// localPaths are what the local API passes on, each to /v1/relay/<path> on
// the server with the relay's token.
var localPaths = []string{"/me", "/domains", "/deployments"}

// localAPI is the relay's API on the machine itself: no auth, because
// anything that reaches it is already on this machine, and it adds the
// relay's token on the way to the server. The server decides what the
// relay may do (only its own deployments, and only once that's turned on
// in the app).
func localAPI(serverURL, token string) (http.Handler, error) {
	up, err := url.Parse(serverURL)
	if err != nil || up.Host == "" {
		return nil, errors.New("bad SUPERMATRIX_SERVER_URL")
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.Out.URL.Path = strings.TrimRight(up.Path, "/") + "/v1/relay" + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = up.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			jsonError(w, http.StatusBadGateway, "can't reach the server: "+err.Error())
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browsers on this machine are the one thing that could reach a
		// localhost port on someone else's behalf: refuse requests from web
		// pages (they carry an Origin) and from DNS-rebound names (a Host
		// that isn't a loopback address).
		if r.Header.Get("Origin") != "" || !loopbackHost(r.Host) {
			jsonError(w, http.StatusForbidden, "only for programs on this machine")
			return
		}
		if !allowedPath(r.URL.Path) {
			jsonError(w, http.StatusNotFound, "not found (try /me, /domains or /deployments)")
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func allowedPath(p string) bool {
	if strings.Contains(p, "..") {
		return false
	}
	for _, pre := range localPaths {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

func loopbackHost(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(b)
}

// serveLocal runs the local API on addr until ctx ends. It refuses to
// listen anywhere but a loopback address: the API has no auth of its own.
func serveLocal(ctx context.Context, addr, serverURL, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if !loopbackHost(host) {
		return errors.New("the local API only listens on a loopback address (127.0.0.1, ::1 or localhost)")
	}
	h, err := localAPI(serverURL, token)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("local API on http://%s (supermatrix-relay help)", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
