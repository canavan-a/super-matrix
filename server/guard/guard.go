// Package guard holds the small request-safety helpers shared by the admin
// API and the edge: the client address and a fixed-window rate limiter.
package guard

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter is a fixed-window rate limiter keyed by anything.
type Limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func NewLimiter(limit int, per time.Duration) *Limiter {
	return &Limiter{limit: limit, window: per, hits: map[string]*window{}}
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.limit
}

// Blocked reports whether key is already over the limit, without counting a
// hit.
func (l *Limiter) Blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	return ok && now.Sub(w.start) <= l.window && w.n >= l.limit
}

// ClientIP is the address a request came from.
//
// When trustProxy is on (another proxy or tunnel on this host forwards to
// us), the real client is in X-Forwarded-For. Only the rightmost entry can be
// trusted: it's the one the proxy appended, and anything a client sends in
// that header itself ends up to its left. The header is only believed on
// loopback connections; a direct connection is keyed by its own address.
func ClientIP(r *http.Request, trustProxy bool) string {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	if trustProxy && isLoopback(remote) {
		if ip := lastForwardedFor(r.Header.Values("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	return remote
}

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}

// lastForwardedFor returns the rightmost address across every
// X-Forwarded-For header, or "" if it isn't an IP.
func lastForwardedFor(values []string) string {
	all := strings.Split(strings.Join(values, ","), ",")
	for i := len(all) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(all[i]); s != "" {
			if p := net.ParseIP(s); p != nil {
				return p.String()
			}
			return ""
		}
	}
	return ""
}

// StripPort lowercases a Host header and drops any :port.
func StripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
