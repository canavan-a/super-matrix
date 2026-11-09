package e2etest

import "testing"

type pathRule struct {
	ID          string
	PathPrefix  string
	IPAllowlist []string
}

func TestPathRules(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	var d dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &d)

	t.Run("crud", func(t *testing.T) {
		var r pathRule
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{
			"pathPrefix": "/secret", "ipAllowlist": []string{"10.0.0.0/8"},
		}, 201, &r)
		if r.PathPrefix != "/secret" || len(r.IPAllowlist) != 1 {
			t.Fatalf("created: %+v", r)
		}
		var upd pathRule
		e.api("PATCH", "/v1/deployments/"+d.ID+"/path-rules/"+r.ID, map[string]any{
			"ipAllowlist": []string{"10.0.0.0/8", "192.168.1.5"},
		}, 200, &upd)
		if len(upd.IPAllowlist) != 2 {
			t.Fatalf("updated: %+v", upd)
		}
		e.api("DELETE", "/v1/deployments/"+d.ID+"/path-rules/"+r.ID, nil, 200, nil)
		e.api("DELETE", "/v1/deployments/"+d.ID+"/path-rules/"+r.ID, nil, 404, nil)
	})

	t.Run("duplicate prefix", func(t *testing.T) {
		var a, b pathRule
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{"pathPrefix": "/dup"}, 201, &a)
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{"pathPrefix": "/dup"}, 409, &b)
		e.api("DELETE", "/v1/deployments/"+d.ID+"/path-rules/"+a.ID, nil, 200, nil)
	})

	t.Run("bad input", func(t *testing.T) {
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{"pathPrefix": "no-leading-slash"}, 400, nil)
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{"pathPrefix": "/trailing/"}, 400, nil)
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{"pathPrefix": "/ok", "ipAllowlist": []string{"not-an-ip"}}, 400, nil)
	})

	t.Run("routing", func(t *testing.T) {
		// Whole-host allow list stays open; only /secret gets an extra,
		// narrower one that excludes the test client's own address
		// (127.0.0.1, which e.get actually connects from).
		var r pathRule
		e.api("POST", "/v1/deployments/"+d.ID+"/path-rules", map[string]any{
			"pathPrefix": "/secret", "ipAllowlist": []string{"10.0.0.0/8"},
		}, 201, &r)

		if res, _ := e.get("app.test", "/secret", nil); res.StatusCode != 403 {
			t.Fatalf("restricted path: %d", res.StatusCode)
		}
		if res, _ := e.get("app.test", "/secret/sub", nil); res.StatusCode != 403 {
			t.Fatalf("restricted path, nested: %d", res.StatusCode)
		}
		if res, _ := e.get("app.test", "/secretly", nil); res.StatusCode != 200 {
			t.Fatalf("lookalike path should NOT match the rule: %d", res.StatusCode)
		}
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 200 {
			t.Fatalf("unrestricted path on same deployment, same IP: %d", res.StatusCode)
		}

		// Widen the rule to include 127.0.0.1: now /secret opens too.
		e.api("PATCH", "/v1/deployments/"+d.ID+"/path-rules/"+r.ID, map[string]any{
			"ipAllowlist": []string{"10.0.0.0/8", "127.0.0.1"},
		}, 200, nil)
		if res, _ := e.get("app.test", "/secret", nil); res.StatusCode != 200 {
			t.Fatalf("widened rule: %d", res.StatusCode)
		}

		// A path rule can't loosen the whole-host allow list: lock the
		// deployment down and confirm /secret (and everything else) is
		// still blocked even though the rule itself would allow 127.0.0.1.
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{"10.0.0.0/8"}}, 200, nil)
		if res, _ := e.get("app.test", "/secret", nil); res.StatusCode != 403 {
			t.Fatalf("host-wide allow list should still win: %d", res.StatusCode)
		}
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 403 {
			t.Fatalf("host-wide allow list should still win on unrestricted path too: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{}}, 200, nil)

		e.api("DELETE", "/v1/deployments/"+d.ID+"/path-rules/"+r.ID, nil, 200, nil)
	})
}
