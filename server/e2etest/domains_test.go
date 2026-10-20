package e2etest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type domain struct {
	ID          string
	Name        string
	Wildcard    bool
	Label       string
	Deployments int
}

func TestDomains(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	// SUPERMATRIX_BASE_DOMAIN ("test") is the first domain.
	var ds []domain
	e.api("GET", "/v1/domains", nil, 200, &ds)
	if len(ds) != 1 || ds[0].Label != "*.test" || !ds[0].Wildcard {
		t.Fatalf("seeded domains: %+v", ds)
	}
	base := ds[0]

	var second, exact domain
	e.api("POST", "/v1/domains", map[string]any{"name": " *.Second.Example. "}, 201, &second)
	if second.Name != "second.example" || !second.Wildcard {
		t.Fatalf("wildcard: %+v", second)
	}
	e.api("POST", "/v1/domains", map[string]any{"name": "status.other.org"}, 201, &exact)
	if exact.Wildcard || exact.Label != "status.other.org" {
		t.Fatalf("exact: %+v", exact)
	}

	t.Run("bad domains", func(t *testing.T) {
		for _, n := range []string{"", "*.", "bad_name.org", "a..b", "*.*.x.org", "-x.org"} {
			e.api("POST", "/v1/domains", map[string]any{"name": n}, 400, nil)
		}
		e.api("POST", "/v1/domains", map[string]any{"name": "127.0.0.1"}, 400, nil) // the admin host
		e.api("POST", "/v1/domains", map[string]any{"name": "second.example"}, 409, nil)
		e.api("POST", "/v1/domains", map[string]any{"name": "*.status.other.org"}, 409, nil) // added as exact already
	})

	mk := func(domainID, sub string, want int) dep {
		var d dep
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "domainId": domainID, "subdomain": sub,
			"targetHost": th, "targetPort": tp, "authMode": "none",
		}, want, &d)
		return d
	}

	t.Run("same subdomain on two wildcard domains", func(t *testing.T) {
		a := mk(base.ID, "app", 201)
		b := mk(second.ID, "app", 201)
		if a.Host != "app.test" || b.Host != "app.second.example" {
			t.Fatalf("hosts %q %q", a.Host, b.Host)
		}
		for _, h := range []string{a.Host, b.Host} {
			if res, body := e.get(h, "/x", nil); res.StatusCode != 200 || !strings.Contains(body, `"path":"/x"`) {
				t.Fatalf("%s: %d %s", h, res.StatusCode, body)
			}
		}
		mk(base.ID, "app", 400) // taken
	})

	t.Run("exact domain", func(t *testing.T) {
		d := mk(exact.ID, "ignored", 201)
		if d.Host != "status.other.org" {
			t.Fatalf("host %q", d.Host)
		}
		if res, _ := e.get("status.other.org", "/", nil); res.StatusCode != 200 {
			t.Fatalf("status %d", res.StatusCode)
		}
		mk(exact.ID, "", 400) // one deployment per exact domain
	})

	t.Run("unknown hosts", func(t *testing.T) {
		for _, h := range []string{"nope.test", "x.status.other.org", "other.org", "app.example"} {
			res := e.req("GET", h, "/", nil, nil)
			readBody(t, res)
			if res.StatusCode != 404 {
				t.Fatalf("%s: %d", h, res.StatusCode)
			}
		}
	})

	t.Run("a domain in use can't be deleted", func(t *testing.T) {
		e.api("DELETE", "/v1/domains/"+exact.ID, nil, 409, nil)
		var tmp domain
		e.api("POST", "/v1/domains", map[string]any{"name": "*.spare.example"}, 201, &tmp)
		e.api("DELETE", "/v1/domains/"+tmp.ID, nil, 200, nil)
		e.api("GET", "/v1/domains", nil, 200, &ds)
		for _, d := range ds {
			if d.ID == exact.ID && d.Deployments != 1 {
				t.Fatalf("count %+v", d)
			}
		}
	})

	t.Run("no domain named needs exactly one domain", func(t *testing.T) {
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "subdomain": "solo", "targetHost": th, "targetPort": tp, "authMode": "none",
		}, 400, nil)
	})

	t.Run("dns check", func(t *testing.T) {
		e.srv.Lookup = func(_ context.Context, host string) ([]string, error) {
			switch {
			case host == "127.0.0.1":
				return []string{"127.0.0.1"}, nil
			case strings.HasSuffix(host, ".test"):
				return []string{"127.0.0.1"}, nil
			case host == "status.other.org":
				return []string{"203.0.113.9"}, nil
			}
			return nil, errors.New("no such host")
		}
		var r struct {
			OK     bool
			Probe  string
			Detail string
		}
		e.api("GET", "/v1/domains/"+base.ID+"/dns", nil, 200, &r)
		if !r.OK || !strings.HasPrefix(r.Probe, "sm-check-") || !strings.HasSuffix(r.Probe, ".test") {
			t.Fatalf("base: %+v", r)
		}
		e.api("GET", "/v1/domains/"+exact.ID+"/dns", nil, 200, &r)
		if r.OK || !strings.Contains(r.Detail, "203.0.113.9") {
			t.Fatalf("exact: %+v", r)
		}
		e.api("GET", "/v1/domains/"+second.ID+"/dns", nil, 200, &r)
		if r.OK || !strings.Contains(r.Detail, "doesn't resolve") {
			t.Fatalf("second: %+v", r)
		}
	})
}
