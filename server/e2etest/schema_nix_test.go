package e2etest

import (
	"encoding/json"
	"strings"
	"testing"

	"main/tunnel"
)

func indent(s, pad string) string {
	return pad + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+pad)
}

func TestSchemaNixServices(t *testing.T) {
	e := newEnv(t)
	f, sock, _ := e.startNixAgent()
	relayID, stop, _ := e.startRelayWith("homelab", withAgent(sock))
	defer stop()
	e.waitFor(func() bool { return e.relay(relayID).Nix.Agent })
	oldID, stopOld, _ := e.startRelayWith("old", func(r *tunnel.Relay) { r.Features = []string{tunnel.FeatureBridgeTCP} })
	defer stopOld()
	_ = oldID

	var tok struct{ Token string }
	e.api("POST", "/v1/api-tokens", map[string]any{"name": "ci"}, 201, &tok)
	ci := tok.Token

	doc := func(relays, extra string) string {
		return `version: 1
nix_services:
  kuma:
    name: Uptime Kuma
    env: {PORT: "3001"}
    relay_env: [DB_PASSWORD]
    flake: |
` + indent(testFlake, "      ") + `
    files:
      config.json: "{}"
` + extra + `    relays:
` + relays
	}

	t.Run("checks", func(t *testing.T) {
		// Not allowed on homelab yet: the readiness reason comes back.
		out := e.schemaReq("POST", "/v1/schema/plan", ci, doc("      homelab: {}\n", ""), 400)
		if !strings.Contains(out, "nix_services.kuma.relays.homelab") || !strings.Contains(out, "turn them on in Relays") {
			t.Fatal(out)
		}
		out = e.schemaReq("POST", "/v1/schema/plan", ci, doc("      old: {}\n", ""), 400)
		if !strings.Contains(out, "too old") {
			t.Fatal(out)
		}
		out = e.schemaReq("POST", "/v1/schema/plan", ci, doc("      nope: {}\n", ""), 400)
		if !strings.Contains(out, `no relay with the slug \"nope\"`) {
			t.Fatal(out)
		}
		// File references are for the CLI.
		out = e.schemaReq("POST", "/v1/schema/plan", ci, "version: 1\nnix_services:\n  x:\n    source: ./nix/x\n", 400)
		if !strings.Contains(out, "supermatrix-admin schema plan|apply -f") {
			t.Fatal(out)
		}
		out = e.schemaReq("POST", "/v1/schema/plan", ci, "version: 1\nnix_services:\n  x:\n    flake: {file: ./flake.nix}\n", 400)
		if !strings.Contains(out, "file references") {
			t.Fatal(out)
		}
		out = e.schemaReq("POST", "/v1/schema/plan", ci, "version: 1\nnix_services:\n  x:\n    run: package\n", 400)
		if !strings.Contains(out, "nix_services.x.flake") {
			t.Fatal(out)
		}
	})

	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": true}, 200, nil)

	var svc nixSvc
	t.Run("apply", func(t *testing.T) {
		p := e.plan(ci, doc("      homelab: {}\n", ""))
		if !p.has("create", "nix_service", "kuma") || !p.has("create", "applied_nix_service", "kuma@homelab") {
			t.Fatalf("plan %+v", p.Ops)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, doc("      homelab: {}\n", ""), 200)
		e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc)
		if len(svc.Applied) != 1 || svc.Managed == nil || svc.Files["config.json"] != "{}" {
			t.Fatalf("service %+v", svc)
		}
		e.waitPhase(svc.Applied[0].ID, "missing-env")
		// The relay's lock waits for the admin; accepted, it isn't the
		// schema's, so applying again keeps it and changes nothing.
		e.waitFor(func() bool { e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc); return svc.PendingLock != "" })
		e.api("POST", "/v1/nix-services/kuma/lock", map[string]any{"accept": true}, 200, &svc)
		if p := e.plan(ci, doc("      homelab: {}\n", "")); len(p.Ops) != 0 {
			t.Fatalf("re-plan %+v", p.Ops)
		}
	})

	t.Run("locked", func(t *testing.T) {
		e.api("PATCH", "/v1/nix-services/kuma", map[string]any{"restart": "no"}, 409, nil)
		e.api("DELETE", "/v1/nix-services/kuma", nil, 409, nil)
		e.api("DELETE", "/v1/applied-nix-services/"+svc.Applied[0].ID, nil, 409, nil)
		e.api("PATCH", "/v1/applied-nix-services/"+svc.Applied[0].ID, map[string]any{"desired": "stopped"}, 409, nil)
		// ui hands desired to the app.
		e.schemaReq("POST", "/v1/schema/apply", ci, doc("      homelab: {ui: [desired]}\n", ""), 200)
		e.api("PATCH", "/v1/applied-nix-services/"+svc.Applied[0].ID, map[string]any{"desired": "stopped"}, 200, nil)
		// A relay slug the schema uses can't be renamed.
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"slug": "lab"}, 409, nil)
	})

	t.Run("a pinned lock", func(t *testing.T) {
		lock := `{"nodes":{"root":{}},"root":"root","version":7,"pinned":true}`
		d := doc("      homelab: {ui: [desired]}\n", "    flake_lock: '"+lock+"'\n")
		p := e.plan(ci, d)
		if !p.has("update", "nix_service", "kuma") {
			t.Fatalf("plan %+v", p.Ops)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, d, 200)
		e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc)
		if !svc.LockPinned || svc.FlakeLock != lock {
			t.Fatalf("lock %q pinned %v", svc.FlakeLock, svc.LockPinned)
		}
	})

	t.Run("export", func(t *testing.T) {
		out := e.schemaReq("GET", "/v1/schema/export?format=json&from=db", ci, "", 200)
		var d struct {
			NixServices map[string]struct {
				Flake     string `json:"flake"`
				FlakeLock string `json:"flake_lock"`
				Relays    map[string]any
			} `json:"nix_services"`
		}
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatal(err, out)
		}
		k := d.NixServices["kuma"]
		if k.Relays["homelab"] == nil || k.FlakeLock == "" || !strings.Contains(k.Flake, "packages.x86_64-linux") {
			t.Fatalf("export: %s", out)
		}
	})

	t.Run("removing the relay tears it down", func(t *testing.T) {
		apID := svc.Applied[0].ID
		d := doc("", "")
		d = strings.TrimSuffix(d, "    relays:\n") + "    relays: {}\n"
		p := e.plan(ci, d)
		if !p.has("delete", "applied_nix_service", "kuma@homelab") {
			t.Fatalf("plan %+v", p.Ops)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, d, 200)
		e.waitFor(func() bool { _, code := e.applied(apID); return code == 404 })
		if f.Unit("kuma") != "" {
			t.Fatal("unit left")
		}
		// And dropping the service deletes it.
		e.schemaReq("POST", "/v1/schema/apply", ci, "version: 1\nnix_services: {}\n", 200)
		e.api("GET", "/v1/nix-services/kuma", nil, 404, nil)
	})
}
