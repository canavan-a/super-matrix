package e2etest

import (
	"slices"
	"testing"
)

type ipGroup struct {
	ID          string
	Name        string
	Entries     []string
	Deployments int
	Bridges     int
}

type depGroups struct {
	ID          string
	IPAllowlist []string
	IPGroups    []string
}

func TestIPGroups(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	var office ipGroup
	e.api("POST", "/v1/ip-groups", map[string]any{"name": " Office ", "entries": []string{"10.0.0.0/8", " 10.0.0.0/8 ", "192.168.1.5"}}, 201, &office)
	if office.Name != "Office" || !slices.Equal(office.Entries, []string{"10.0.0.0/8", "192.168.1.5/32"}) {
		t.Fatalf("created: %+v", office)
	}

	t.Run("validation", func(t *testing.T) {
		e.api("POST", "/v1/ip-groups", map[string]any{"name": "office", "entries": []string{"10.1.0.0/16"}}, 409, nil)
		e.api("POST", "/v1/ip-groups", map[string]any{"name": "empty", "entries": []string{}}, 400, nil)
		e.api("POST", "/v1/ip-groups", map[string]any{"name": "bad", "entries": []string{"not-an-ip"}}, 400, nil)
		e.api("POST", "/v1/ip-groups", map[string]any{"name": "", "entries": []string{"10.0.0.1"}}, 400, nil)
		e.api("PATCH", "/v1/ip-groups/"+office.ID, map[string]any{"entries": []string{}}, 400, nil)
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "subdomain": "nogroup", "targetHost": th, "targetPort": tp, "authMode": "none", "ipGroups": []string{"nope"},
		}, 400, nil)
	})

	var d depGroups
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "app", "targetHost": th, "targetPort": tp, "authMode": "none", "ipGroups": []string{office.ID, office.ID},
	}, 201, &d)
	if !slices.Equal(d.IPGroups, []string{office.ID}) {
		t.Fatalf("deployment groups: %+v", d)
	}

	t.Run("group alone restricts", func(t *testing.T) {
		// The test client is 127.0.0.1, not in the group.
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 403 {
			t.Fatalf("outside the group: %d", res.StatusCode)
		}
	})

	t.Run("allow list and group combine", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{"127.0.0.1"}}, 200, &d)
		if !slices.Equal(d.IPGroups, []string{office.ID}) {
			t.Fatalf("a PATCH without ipGroups keeps them: %+v", d)
		}
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 200 {
			t.Fatalf("on the allow list: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipAllowlist": []string{}}, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 403 {
			t.Fatalf("back to the group only: %d", res.StatusCode)
		}
	})

	t.Run("editing the group applies everywhere", func(t *testing.T) {
		e.api("PATCH", "/v1/ip-groups/"+office.ID, map[string]any{"entries": []string{"10.0.0.0/8", "127.0.0.0/8"}}, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 200 {
			t.Fatalf("added to the group: %d", res.StatusCode)
		}
		e.api("PATCH", "/v1/ip-groups/"+office.ID, map[string]any{"entries": []string{"10.0.0.0/8"}}, 200, nil)
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 403 {
			t.Fatalf("taken out of the group: %d", res.StatusCode)
		}
	})

	t.Run("in use", func(t *testing.T) {
		var gs []ipGroup
		e.api("GET", "/v1/ip-groups", nil, 200, &gs)
		if len(gs) != 1 || gs[0].Deployments != 1 {
			t.Fatalf("list: %+v", gs)
		}
		e.api("DELETE", "/v1/ip-groups/"+office.ID, nil, 409, nil)
	})

	t.Run("deleting a deployment frees its groups", func(t *testing.T) {
		var other depGroups
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "subdomain": "other", "targetHost": th, "targetPort": tp, "authMode": "none", "ipGroups": []string{office.ID},
		}, 201, &other)
		e.api("DELETE", "/v1/deployments/"+other.ID, nil, 200, nil)
		var g ipGroup
		e.api("GET", "/v1/ip-groups/"+office.ID, nil, 200, &g)
		if g.Deployments != 1 {
			t.Fatalf("after delete: %+v", g)
		}
	})

	t.Run("removed", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"ipGroups": []string{}}, 200, &d)
		if len(d.IPGroups) != 0 {
			t.Fatalf("cleared: %+v", d)
		}
		if res, _ := e.get("app.test", "/", nil); res.StatusCode != 200 {
			t.Fatalf("open again: %d", res.StatusCode)
		}
		e.api("DELETE", "/v1/ip-groups/"+office.ID, nil, 200, nil)
		e.api("GET", "/v1/ip-groups/"+office.ID, nil, 404, nil)
	})
}
