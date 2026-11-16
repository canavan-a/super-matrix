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
	_, piSock, _ := e.startNixAgent()
	other, stopOther, _ := e.startRelayWith("pi", withAgent(piSock))
	defer stopOther()
	e.waitFor(func() bool { return e.relay(relayID).Nix.Agent && e.relay(other).Nix.Agent })
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": true}, 200, nil)
	e.api("PATCH", "/v1/relays/"+other, map[string]any{"allowNixServices": true}, 200, nil)

	// An admin service, which the relay never sees.
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
	if mine.OwnerRelayID != relayID || mine.OwnerRelayName != "homelab" {
		t.Fatalf("owner: %+v", mine)
	}
	var list []nixSvc
	e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 200, &list)
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("a relay sees only its own: %+v", list)
	}
	e.relayAPI(tok, "GET", "/v1/relay/nix-services/"+admin.ID, nil, 404, nil)
	e.relayAPI(tok, "POST", "/v1/relay/nix-services/"+admin.ID+"/applied", map[string]any{}, 404, nil)

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
	// Its own service's lock is taken at once.
	e.waitFor(func() bool {
		e.relayAPI(tok, "GET", "/v1/relay/nix-services/mine", nil, 200, &mine)
		return mine.FlakeLock != ""
	})
	if mine.PendingLock != "" {
		t.Fatalf("pending on its own service: %+v", mine)
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

	// The admin can't put it on another relay without adopting it.
	e.api("POST", "/v1/nix-services/mine/applied", map[string]any{"relayId": other}, 409, nil)
	// An admin service on this relay isn't the relay's to manage.
	var adminAp nixApplied
	e.api("POST", "/v1/nix-services/"+admin.ID+"/applied", map[string]any{"relayId": relayID}, 201, &adminAp)
	e.relayAPI(tok, "PATCH", "/v1/relay/applied-nix-services/"+adminAp.ID, map[string]any{"desired": "stopped"}, 404, nil)
	e.relayAPI(tok, "DELETE", "/v1/relay/applied-nix-services/"+adminAp.ID, nil, 404, nil)

	t.Run("adopted", func(t *testing.T) {
		e.api("POST", "/v1/nix-services/mine/adopt", nil, 200, &mine)
		if mine.OwnerRelayID != "" {
			t.Fatalf("still the relay's: %+v", mine)
		}
		e.relayAPI(tok, "GET", "/v1/relay/nix-services/mine", nil, 404, nil)
		e.relayAPI(tok, "DELETE", "/v1/relay/applied-nix-services/"+ap.ID, nil, 404, nil)
		var onPi nixApplied
		e.api("POST", "/v1/nix-services/mine/applied", map[string]any{"relayId": other}, 201, &onPi)
		e.waitPhase(onPi.ID, "running")
		e.api("POST", "/v1/nix-services/mine/adopt", nil, 409, nil)
	})

	t.Run("its own, torn down", func(t *testing.T) {
		var gone nixSvc
		e.relayAPI(tok, "POST", "/v1/relay/nix-services", map[string]any{"slug": "gone", "files": map[string]string{"flake.nix": testFlake}}, 201, &gone)
		var gap nixApplied
		e.relayAPI(tok, "POST", "/v1/relay/nix-services/gone/applied", nil, 201, &gap)
		e.waitPhase(gap.ID, "running")
		e.relayAPI(tok, "DELETE", "/v1/relay/nix-services/gone", nil, 200, nil)
		e.waitFor(func() bool { _, code := e.applied(gap.ID); return code == 404 })
		if f.Unit("gone") != "" {
			t.Fatal("not torn down")
		}
	})

	t.Run("turned off", func(t *testing.T) {
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"manageNixServices": false}, 200, nil)
		var out map[string]string
		e.relayAPI(tok, "GET", "/v1/relay/nix-services", nil, 403, &out)
		if !strings.Contains(out["error"], "Manage its Nix services") {
			t.Fatalf("error: %v", out)
		}
	})
}
