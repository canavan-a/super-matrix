package e2etest

import (
	"strings"
	"testing"
)

func TestRelayNixServices(t *testing.T) {
	e := newEnv(t)
	f, sock, _ := e.startNixAgent()
	relayID, stop, _ := e.startRelayWith("homelab", withAgent(sock))
	defer stop()
	tok := e.e2eRelayToken
	// Another machine, with its own agent.
	piF, piSock, _ := e.startNixAgent()
	other, stopOther, _ := e.startRelayWith("pi", withAgent(piSock))
	defer stopOther()
	e.waitFor(func() bool { return e.relay(relayID).Nix.Agent && e.relay(other).Nix.Agent })
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": true}, 200, nil)
	e.api("PATCH", "/v1/relays/"+other, map[string]any{"allowNixServices": true}, 200, nil)

	// An admin service: relays see it too.
	var admin nixSvc
	e.api("POST", "/v1/nix-services", map[string]any{"name": "Admin's", "files": map[string]string{"flake.nix": testFlake}}, 201, &admin)

	def := map[string]any{"name": "Relay's", "slug": "mine", "files": map[string]string{"flake.nix": testFlake}}

	// Off by default, like managing deployments.
	e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 403, nil)
	e.relayAPI(tok, "POST", "/v1/relay/nix-services", def, 403, nil)
	var me struct{ ManageNixServices bool }
	e.relayAPI(tok, "GET", "/v1/relay/me", nil, 200, &me)
	if me.ManageNixServices {
		t.Fatal("on by default")
	}
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"manageNixServices": true}, 200, nil)

	var mine nixSvc
	e.relayAPI(tok, "POST", "/v1/relay/nix-services", def, 201, &mine)
	// Every service is everyone's: the relay sees the admin's, and the
	// admin sees the relay's.
	var list []nixSvc
	e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 200, &list)
	if len(list) != 2 {
		t.Fatalf("a relay sees them all: %+v", list)
	}
	e.api("GET", "/v1/nix-services", nil, 200, &list)
	if len(list) != 2 {
		t.Fatalf("the admin sees them all: %+v", list)
	}

	// Only on itself.
	e.relayAPI(tok, "POST", "/v1/relay/nix-services/mine/applied", map[string]any{"relayId": other}, 403, nil)
	var ap nixApplied
	e.relayAPI(tok, "POST", "/v1/relay/nix-services/mine/applied", map[string]any{}, 201, &ap)
	if ap.RelayID != relayID {
		t.Fatalf("applied to %s", ap.RelayID)
	}
	e.waitPhase(ap.ID, "running")
	if !f.Active("mine") {
		t.Fatal("not running")
	}
	// Running only here, on a relay that may change it anyway, its lock is
	// taken at once.
	e.waitFor(func() bool {
		e.relayAPI(tok, "GET", "/v1/relay/nix-services/mine", nil, 200, &mine)
		return mine.FlakeLock != ""
	})
	if mine.PendingLock != "" {
		t.Fatalf("pending with no other relay running it: %+v", mine)
	}
	var aps []nixApplied
	e.relayAPI(tok, "GET", "/v1/relay/applied-nix-services", nil, 200, &aps)
	if len(aps) != 1 || aps[0].ID != ap.ID {
		t.Fatalf("applied: %+v", aps)
	}
	e.relayAPI(tok, "PATCH", "/v1/relay/applied-nix-services/"+ap.ID, map[string]any{"desired": "stopped"}, 200, nil)
	e.waitPhase(ap.ID, "stopped")
	e.relayAPI(tok, "PATCH", "/v1/relay/applied-nix-services/"+ap.ID, map[string]any{"desired": "running"}, 200, nil)
	e.waitPhase(ap.ID, "running")

	// The admin puts the relay's service on another relay as it is.
	var onPi nixApplied
	e.api("POST", "/v1/nix-services/mine/applied", map[string]any{"relayId": other, "env": map[string]string{"SECRET": "pi-only"}}, 201, &onPi)
	e.waitPhase(onPi.ID, "running")
	if !piF.Active("mine") {
		t.Fatal("not running on pi")
	}
	// The relay sees the service, but only itself where it's applied (not
	// pi's env), on every route that returns it.
	for _, raw := range []string{
		e.relayAPI(tok, "GET", "/v1/relay/nix-services/mine", nil, 200, nil),
		e.relayAPI(tok, "PATCH", "/v1/relay/nix-services/mine", map[string]any{"name": "Relay's"}, 200, nil),
	} {
		if strings.Contains(raw, "pi-only") || strings.Contains(raw, onPi.ID) {
			t.Fatalf("another relay's applied service showed: %s", raw)
		}
	}
	var adminView struct{ Applied []nixApplied }
	e.api("GET", "/v1/nix-services/mine", nil, 200, &adminView)
	if len(adminView.Applied) != 2 {
		t.Fatalf("the admin sees both: %+v", adminView.Applied)
	}
	// The relay doesn't see or manage what runs on another relay.
	e.relayAPI(tok, "GET", "/v1/relay/applied-nix-services", nil, 200, &aps)
	if len(aps) != 1 || aps[0].ID != ap.ID {
		t.Fatalf("applied elsewhere showed up: %+v", aps)
	}
	e.relayAPI(tok, "DELETE", "/v1/relay/applied-nix-services/"+onPi.ID, nil, 404, nil)

	// It applies the admin's service to itself, and manages it there.
	var adminAp nixApplied
	e.relayAPI(tok, "POST", "/v1/relay/nix-services/"+admin.ID+"/applied", nil, 201, &adminAp)
	e.waitPhase(adminAp.ID, "running")
	e.relayAPI(tok, "PATCH", "/v1/relay/applied-nix-services/"+adminAp.ID, map[string]any{"desired": "stopped"}, 200, nil)
	e.waitPhase(adminAp.ID, "stopped")
	e.relayAPI(tok, "DELETE", "/v1/relay/applied-nix-services/"+adminAp.ID, nil, 200, nil)

	t.Run("deleted, torn down everywhere", func(t *testing.T) {
		var gone nixSvc
		e.relayAPI(tok, "POST", "/v1/relay/nix-services", map[string]any{"slug": "gone", "files": map[string]string{"flake.nix": testFlake}}, 201, &gone)
		var gap, piGap nixApplied
		e.relayAPI(tok, "POST", "/v1/relay/nix-services/gone/applied", nil, 201, &gap)
		e.api("POST", "/v1/nix-services/gone/applied", map[string]any{"relayId": other}, 201, &piGap)
		e.waitPhase(gap.ID, "running")
		e.waitPhase(piGap.ID, "running")
		e.relayAPI(tok, "DELETE", "/v1/relay/nix-services/gone", nil, 200, nil)
		e.waitFor(func() bool {
			_, code := e.applied(gap.ID)
			_, piCode := e.applied(piGap.ID)
			return code == 404 && piCode == 404
		})
		if f.Unit("gone") != "" || piF.Unit("gone") != "" {
			t.Fatal("not torn down")
		}
	})

	t.Run("a deleted relay leaves its services", func(t *testing.T) {
		var kept nixSvc
		e.relayAPI(tok, "POST", "/v1/relay/nix-services", map[string]any{"slug": "kept", "files": map[string]string{"flake.nix": testFlake}}, 201, &kept)
		e.api("DELETE", "/v1/applied-nix-services/"+ap.ID, nil, 200, nil)
		e.waitFor(func() bool { _, code := e.applied(ap.ID); return code == 404 })
		e.api("DELETE", "/v1/relays/"+relayID, nil, 200, nil)
		e.api("GET", "/v1/nix-services/kept", nil, 200, &kept)
		e.api("GET", "/v1/nix-services/mine", nil, 200, &mine)
	})
}

func TestRelayNixServicesOff(t *testing.T) {
	e := newEnv(t)
	_, sock, _ := e.startNixAgent()
	relayID, stop, _ := e.startRelayWith("homelab", withAgent(sock))
	defer stop()
	tok := e.e2eRelayToken
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"manageNixServices": true}, 200, nil)
	e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 200, nil)
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"manageNixServices": false}, 200, nil)
	var out map[string]string
	e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 403, &out)
	if !strings.Contains(out["error"], "Manage its Nix services") {
		t.Fatalf("error: %v", out)
	}
}
