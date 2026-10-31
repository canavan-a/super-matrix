package e2etest

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeCerts records which hosts were warmed.
type fakeCerts struct {
	mu     sync.Mutex
	warmed []string
}

func (f *fakeCerts) Warm(host string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warmed = append(f.warmed, host)
}

func (f *fakeCerts) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, h := range f.warmed {
		if h == host {
			n++
		}
	}
	return n
}

func TestCertRetry(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &d)
	path := "/v1/deployments/" + d.ID + "/cert"

	t.Run("tls off", func(t *testing.T) {
		e.api("POST", path, nil, 409, nil)
	})

	fc := &fakeCerts{}
	e.srv.Certs = fc
	now := time.Now()
	e.srv.SetClock(func() time.Time { return now })

	t.Run("retry warms the host and clears the error", func(t *testing.T) {
		e.srv.CertStatus(d.Host, time.Time{}, errors.New("no viable challenge type found"))
		e.api("POST", path, nil, 202, nil)
		if fc.count(d.Host) != 1 {
			t.Fatalf("warmed %v", fc.warmed)
		}
		var got struct{ CertStatus, CertError string }
		e.api("GET", "/v1/deployments/"+d.ID, nil, 200, &got)
		if got.CertStatus != "pending" || got.CertError != "" {
			t.Fatalf("after retry: %+v", got)
		}
	})

	t.Run("cooldown", func(t *testing.T) {
		e.api("POST", path, nil, 429, nil)
		now = now.Add(16 * time.Minute)
		e.api("POST", path, nil, 202, nil)
		if fc.count(d.Host) != 2 {
			t.Fatalf("warmed %v", fc.warmed)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		now = now.Add(16 * time.Minute)
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"enabled": false}, 200, nil)
		e.api("POST", path, nil, 409, nil)
	})

	t.Run("unknown deployment", func(t *testing.T) {
		e.api("POST", "/v1/deployments/nope/cert", nil, 404, nil)
	})
}
