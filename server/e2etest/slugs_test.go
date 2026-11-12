package e2etest

import "testing"

type slugged struct {
	ID   string
	Name string
	Slug string
}

func TestSlugs(t *testing.T) {
	e := newEnv(t)

	newRelay := func(body map[string]any, want int) slugged {
		t.Helper()
		var out struct{ Relay slugged }
		e.api("POST", "/v1/relays", body, want, &out)
		return out.Relay
	}

	a := newRelay(map[string]any{"name": "Home Lab"}, 201)
	b := newRelay(map[string]any{"name": "home lab"}, 201)
	if a.Slug != "home-lab" || b.Slug != "home-lab-2" {
		t.Fatalf("made from names: %q %q", a.Slug, b.Slug)
	}
	c := newRelay(map[string]any{"name": "Garage", "slug": " Garage-Pi "}, 201)
	if c.Slug != "garage-pi" {
		t.Fatalf("asked for: %q", c.Slug)
	}

	t.Run("asking for a taken or bad slug fails", func(t *testing.T) {
		newRelay(map[string]any{"name": "x", "slug": "garage-pi"}, 409)
		newRelay(map[string]any{"name": "x", "slug": "no spaces"}, 400)
		newRelay(map[string]any{"name": "x", "slug": "-dash"}, 400)
		e.api("PATCH", "/v1/relays/"+a.ID, map[string]any{"slug": "garage-pi"}, 409, nil)
		e.api("PATCH", "/v1/relays/"+a.ID, map[string]any{"slug": ""}, 400, nil)
	})

	t.Run("renaming keeps the slug; the slug changes on its own", func(t *testing.T) {
		var r slugged
		e.api("PATCH", "/v1/relays/"+a.ID, map[string]any{"name": "Basement"}, 200, &r)
		if r.Name != "Basement" || r.Slug != "home-lab" {
			t.Fatalf("after rename: %+v", r)
		}
		e.api("PATCH", "/v1/relays/"+a.ID, map[string]any{"slug": "basement"}, 200, &r)
		if r.Slug != "basement" {
			t.Fatalf("after new slug: %+v", r)
		}
		// Its own slug isn't a clash.
		e.api("PATCH", "/v1/relays/"+a.ID, map[string]any{"slug": "basement"}, 200, &r)
	})

	t.Run("bridges", func(t *testing.T) {
		// Bridges need relays that have connected and said they can.
		from, stop1 := e.startRelay("from")
		defer stop1()
		to, stop2 := e.startRelay("to")
		defer stop2()
		var br slugged
		e.api("POST", "/v1/bridges", map[string]any{
			"name": "Postgres DB", "protocol": "tcp", "fromRelayId": from, "listenPort": 15432, "toRelayId": to, "targetHost": "db",
		}, 201, &br)
		if br.Slug != "postgres-db" {
			t.Fatalf("bridge slug: %+v", br)
		}
		var br2 slugged
		e.api("POST", "/v1/bridges", map[string]any{
			"name": "other", "slug": "postgres-db", "protocol": "tcp", "fromRelayId": from, "listenPort": 15433, "toRelayId": to, "targetHost": "db",
		}, 409, nil)
		e.api("PATCH", "/v1/bridges/"+br.ID, map[string]any{"slug": " "}, 400, nil)
		e.api("PATCH", "/v1/bridges/"+br.ID, map[string]any{"slug": "pg"}, 200, &br2)
		if br2.Slug != "pg" {
			t.Fatalf("bridge after new slug: %+v", br2)
		}
		e.api("PATCH", "/v1/bridges/"+br.ID, map[string]any{"name": "renamed"}, 200, &br2)
		if br2.Slug != "pg" {
			t.Fatalf("bridge slug changed on rename: %+v", br2)
		}
	})
}
