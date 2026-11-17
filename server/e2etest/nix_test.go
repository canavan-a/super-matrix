package e2etest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"main/nixagent"
	"main/tunnel"
)

const testFlake = `{
  outputs = { self }: { packages.x86_64-linux.default = derivation { name = "kuma"; system = "x86_64-linux"; builder = "/bin/sh"; }; };
}`

type nixRelay struct {
	ID               string
	Name             string
	AllowNixServices bool
	NixServices      int
	Nix              struct {
		Supported, Agent, Allowed, Ready bool
		Step, Reason                     string
		Host                             *tunnel.NixHost
	}
}

type nixApplied struct {
	ID, RelayID, Phase, Serving, Previous, Desired, LastError string
	Removing, UpToDate, Failing                               bool
	MissingEnv                                                []string
	Env                                                       map[string]string
}

type nixSvc struct {
	ID, Slug, FlakeLock          string
	PendingLock, PendingLockFrom string
	LockPinned                   bool
	Files                        map[string]string
	Applied                      []nixApplied
	Managed                      *struct{ Fields []string }
}

// startNixAgent runs a Nix agent (with a fake runner) on a socket of its
// own, the way supermatrix-relay nix-agent does on a machine.
func (e *env) startNixAgent() (*nixagent.Fake, string, string) {
	e.t.Helper()
	dir, err := os.MkdirTemp("", "smx-nix")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { os.RemoveAll(dir) })
	f := nixagent.NewFake()
	m := nixagent.NewManager(nixagent.Config{Dir: dir + "/agent", EnvDir: dir + "/env", StateRoot: dir + "/state", Runner: f,
		Logf: func(string, ...any) {}, MonitorEvery: 20 * time.Millisecond, StartGrace: 10 * time.Millisecond})
	_ = os.MkdirAll(dir+"/env", 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		e.t.Fatal(err)
	}
	sock := filepath.Join(dir, "a.sock")
	ln, err := nixagent.Listen(sock, "")
	if err != nil {
		e.t.Fatal(err)
	}
	go m.Serve(ctx, ln)
	return f, sock, dir
}

func withAgent(sock string) func(*tunnel.Relay) {
	return func(r *tunnel.Relay) {
		c := nixagent.NewClient(sock, func(string, ...any) {})
		ctx, cancel := context.WithCancel(context.Background())
		_ = cancel
		go c.Run(ctx)
		r.Nix = c
	}
}

func (e *env) relay(id string) nixRelay {
	e.t.Helper()
	var rs []nixRelay
	e.api("GET", "/v1/relays", nil, 200, &rs)
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	e.t.Fatalf("no relay %s", id)
	return nixRelay{}
}

func (e *env) applied(id string) (nixApplied, int) {
	e.t.Helper()
	var ap nixApplied
	res := e.req("GET", "127.0.0.1", "/v1/applied-nix-services/"+id, nil, http.Header{"Authorization": {"Bearer " + e.token}})
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode == 200 {
		_ = jsonUnmarshal(b, &ap)
	}
	return ap, res.StatusCode
}

func (e *env) waitPhase(id, phase string) nixApplied {
	e.t.Helper()
	var ap nixApplied
	e.waitFor(func() bool {
		ap, _ = e.applied(id)
		return ap.Phase == phase
	})
	return ap
}

func TestNixReadiness(t *testing.T) {
	e := newEnv(t)

	// A relay from before Nix services: it has to be updated first.
	old, stopOld, _ := e.startRelayWith("old", func(r *tunnel.Relay) { r.Features = []string{tunnel.FeatureBridgeTCP, tunnel.FeatureBridgeUDP} })
	defer stopOld()
	e.waitFor(func() bool { return e.relay(old).Nix.Step == "update" })
	if r := e.relay(old); r.Nix.Supported || !strings.Contains(r.Nix.Reason, "too old") {
		t.Fatalf("old relay: %+v", r.Nix)
	}
	e.api("PATCH", "/v1/relays/"+old, map[string]any{"allowNixServices": true}, 409, nil)

	// A current relay without an agent on its machine.
	noAgent, stopNA, _ := e.startRelayWith("bare", withAgent(filepath.Join(t.TempDir(), "none.sock")))
	defer stopNA()
	e.waitFor(func() bool { return e.relay(noAgent).Nix.Supported })
	if r := e.relay(noAgent); r.Nix.Step != "agent" || r.Nix.Agent || !strings.Contains(r.Nix.Reason, "nixServices.enable") {
		t.Fatalf("no agent: %+v", r.Nix)
	}
	// It may be allowed ahead of time, but isn't ready.
	e.api("PATCH", "/v1/relays/"+noAgent, map[string]any{"allowNixServices": true}, 200, nil)
	if r := e.relay(noAgent); r.Nix.Ready || r.Nix.Step != "agent" {
		t.Fatalf("allowed without an agent: %+v", r.Nix)
	}

	// With the agent: ready once allowed.
	_, sock, _ := e.startNixAgent()
	id, stop, _ := e.startRelayWith("homelab", withAgent(sock))
	defer stop()
	e.waitFor(func() bool { return e.relay(id).Nix.Agent })
	r := e.relay(id)
	if r.Nix.Step != "allow" || r.Nix.Host == nil || !r.Nix.Host.OK || r.Nix.Host.System != "x86_64-linux" {
		t.Fatalf("with agent: %+v", r.Nix)
	}
	var svc nixSvc
	e.api("POST", "/v1/nix-services", map[string]any{"name": "Kuma", "files": map[string]string{"flake.nix": testFlake}}, 201, &svc)
	e.api("POST", "/v1/nix-services/"+svc.ID+"/applied", map[string]any{"relayId": id}, 409, nil)
	e.api("PATCH", "/v1/relays/"+id, map[string]any{"allowNixServices": true}, 200, nil)
	if r := e.relay(id); !r.Nix.Ready {
		t.Fatalf("not ready: %+v", r.Nix)
	}
	e.api("POST", "/v1/nix-services/"+svc.ID+"/applied", map[string]any{"relayId": noAgent}, 409, nil)
	e.api("POST", "/v1/nix-services/"+svc.ID+"/applied", map[string]any{"relayId": id}, 201, nil)
	// It can't be turned off (or the relay deleted) while services run.
	e.api("PATCH", "/v1/relays/"+id, map[string]any{"allowNixServices": false}, 409, nil)
	e.api("DELETE", "/v1/relays/"+id, nil, 409, nil)
}

func TestNixServices(t *testing.T) {
	e := newEnv(t)
	f, sock, dir := e.startNixAgent()
	relayID, stop, _ := e.startRelayWith("homelab", withAgent(sock))
	defer stop()
	e.waitFor(func() bool { return e.relay(relayID).Nix.Agent })
	e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": true}, 200, nil)

	e.api("POST", "/v1/nix-services", map[string]any{"name": "x"}, 400, nil)
	e.api("POST", "/v1/nix-services", map[string]any{"name": "x", "files": map[string]string{"flake.nix": testFlake, "../etc/passwd": "x"}}, 400, nil)
	e.api("POST", "/v1/nix-services", map[string]any{"name": "x", "run": "devshell", "files": map[string]string{"flake.nix": testFlake}}, 400, nil)

	var svc nixSvc
	e.api("POST", "/v1/nix-services", map[string]any{
		"name": "Uptime Kuma", "slug": "kuma", "env": map[string]string{"PORT": "3001"},
		"files": map[string]string{"flake.nix": testFlake, "config.json": "{}"},
	}, 201, &svc)
	var ap nixApplied
	e.api("POST", "/v1/nix-services/kuma/applied", map[string]any{"relayId": "homelab"}, 201, &ap)
	if ap.Phase != "pending" {
		t.Fatalf("new applied: %+v", ap)
	}
	e.api("POST", "/v1/nix-services/kuma/applied", map[string]any{"relayId": relayID}, 409, nil)

	ap = e.waitPhase(ap.ID, "running")
	if !strings.HasPrefix(ap.Serving, "/nix/store/") || !f.Active("kuma") || !strings.Contains(f.Unit("kuma"), `Environment="PORT=3001"`) {
		t.Fatalf("running: %+v\n%s", ap, f.Unit("kuma"))
	}
	// The relay's lock waits for the admin (it's not the relay's service);
	// accepted, it's the service's, and what runs is up to date.
	e.waitFor(func() bool { e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc); return svc.PendingLock != "" })
	if svc.FlakeLock != "" || svc.PendingLockFrom != "homelab" {
		t.Fatalf("pending: %+v", svc)
	}
	e.api("POST", "/v1/nix-services/kuma/lock", map[string]any{"accept": true}, 200, &svc)
	e.waitFor(func() bool {
		e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc)
		ap, _ = e.applied(ap.ID)
		return svc.FlakeLock != "" && svc.PendingLock == "" && ap.UpToDate
	})
	e.api("POST", "/v1/nix-services/kuma/lock", map[string]any{"accept": true}, 409, nil)
	if svc.LockPinned || svc.Files["config.json"] != "{}" {
		t.Fatalf("service: %+v", svc)
	}
	var events []struct{ Kind, Message, Log string }
	e.api("GET", "/v1/applied-nix-services/"+ap.ID+"/events", nil, 200, &events)
	kinds := []string{}
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	for _, want := range []string{"applied", "building", "build-ok", "started", "lock-pending"} {
		if !strings.Contains(strings.Join(kinds, ","), want) {
			t.Fatalf("events %v: no %s", kinds, want)
		}
	}

	t.Run("logs", func(t *testing.T) {
		res := e.req("GET", "127.0.0.1", "/v1/applied-nix-services/"+ap.ID+"/logs?lines=50", nil, http.Header{"Authorization": {"Bearer " + e.token}})
		body := readBody(t, res)
		if res.StatusCode != 200 || !strings.Contains(body, "journal of smx-kuma.service (last 50 lines)") {
			t.Fatalf("journal: %d %q", res.StatusCode, body)
		}
		res = e.req("GET", "127.0.0.1", "/v1/applied-nix-services/"+ap.ID+"/logs?build=1", nil, http.Header{"Authorization": {"Bearer " + e.token}})
		body = readBody(t, res)
		if res.StatusCode != 200 || !strings.Contains(body, "building kuma (fake)") {
			t.Fatalf("build log: %d %q", res.StatusCode, body)
		}
	})

	t.Run("a failed build", func(t *testing.T) {
		f.BuildErr = func(s tunnel.NixService) error {
			if strings.Contains(s.Files["flake.nix"], "broken") {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		e.api("PATCH", "/v1/nix-services/kuma", map[string]any{"files": map[string]string{"flake.nix": "broken"}}, 200, nil)
		got := e.waitPhase(ap.ID, "build-failed")
		if !got.Failing || got.Serving != ap.Serving || got.UpToDate || !f.Active("kuma") {
			t.Fatalf("failed build: %+v", got)
		}
		var rel nixRelay
		e.waitFor(func() bool { rel = e.relay(relayID); return rel.NixServices == 1 })
		e.api("PATCH", "/v1/nix-services/kuma", map[string]any{"files": map[string]string{"flake.nix": testFlake}}, 200, nil)
		e.waitPhase(ap.ID, "running")
	})

	t.Run("missing relay env", func(t *testing.T) {
		e.api("PATCH", "/v1/nix-services/kuma", map[string]any{"relayEnv": []string{"DB_PASSWORD"}}, 200, nil)
		got := e.waitPhase(ap.ID, "missing-env")
		if len(got.MissingEnv) != 1 || got.MissingEnv[0] != "DB_PASSWORD" {
			t.Fatalf("missing: %+v", got)
		}
		_ = os.WriteFile(filepath.Join(dir, "env", "kuma.env"), []byte("DB_PASSWORD=hunter2\n"), 0o600)
		e.waitPhase(ap.ID, "running")
	})

	t.Run("stop, start and actions", func(t *testing.T) {
		e.api("PATCH", "/v1/applied-nix-services/"+ap.ID, map[string]any{"desired": "stopped"}, 200, nil)
		e.waitPhase(ap.ID, "stopped")
		e.api("PATCH", "/v1/applied-nix-services/"+ap.ID, map[string]any{"desired": "running", "env": map[string]string{"PORT": "3002"}}, 200, nil)
		e.waitPhase(ap.ID, "running")
		if !strings.Contains(f.Unit("kuma"), `Environment="PORT=3002"`) {
			t.Fatalf("override: %s", f.Unit("kuma"))
		}
		starts := f.StartCount("kuma")
		e.api("POST", "/v1/applied-nix-services/"+ap.ID+"/restart", nil, 200, nil)
		e.waitFor(func() bool { return f.StartCount("kuma") > starts })
		e.api("POST", "/v1/applied-nix-services/"+ap.ID+"/bogus", nil, 404, nil)
	})

	t.Run("relock", func(t *testing.T) {
		e.api("POST", "/v1/nix-services/kuma/relock", nil, 200, &svc)
		if svc.FlakeLock != "" {
			t.Fatal("lock kept")
		}
		e.waitFor(func() bool { e.api("GET", "/v1/nix-services/kuma", nil, 200, &svc); return svc.PendingLock != "" })
		// Turned down, it goes, and the lock stays as it was (none).
		e.api("POST", "/v1/nix-services/kuma/lock", map[string]any{"accept": false}, 200, &svc)
		if svc.PendingLock != "" || svc.FlakeLock != "" {
			t.Fatalf("turned down: %+v", svc)
		}
	})

	t.Run("teardown", func(t *testing.T) {
		e.api("DELETE", "/v1/applied-nix-services/"+ap.ID, nil, 200, nil)
		e.waitFor(func() bool { _, code := e.applied(ap.ID); return code == 404 })
		if f.Unit("kuma") != "" {
			t.Fatal("the unit is still there")
		}
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": false}, 200, nil)
	})

	t.Run("removed while the relay is away", func(t *testing.T) {
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"allowNixServices": true}, 200, nil)
		var ap2 nixApplied
		e.api("POST", "/v1/nix-services/kuma/applied", map[string]any{"relayId": relayID}, 201, &ap2)
		e.waitPhase(ap2.ID, "running")
		stop()
		e.waitFor(func() bool { return !e.relayOnline(relayID) })
		e.api("DELETE", "/v1/nix-services/kuma", nil, 200, nil)
		got, _ := e.applied(ap2.ID)
		if !got.Removing || got.Phase != "tearing-down" {
			t.Fatalf("while away: %+v", got)
		}
		_, stop2, _ := e.startRelayWithToken(e.e2eRelayToken, withAgent(sock))
		defer stop2()
		e.waitFor(func() bool { _, code := e.applied(ap2.ID); return code == 404 })
		if f.Unit("kuma") != "" {
			t.Fatal("not torn down after reconnecting")
		}
	})
}

func (e *env) relayOnline(id string) bool {
	var st struct{ Relays map[string]any }
	e.api("GET", "/v1/status", nil, 200, &st)
	_, ok := st.Relays[id]
	return ok
}

// startRelayWithToken starts another relay process for an existing relay.
func (e *env) startRelayWithToken(token string, opt func(*tunnel.Relay)) (*tunnel.Relay, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &tunnel.Relay{ServerURL: "http://" + e.addr, Token: token, Version: "test", HealthEvery: time.Hour, Logf: func(string, ...any) {}}
	if opt != nil {
		opt(r)
	}
	go r.Run(ctx)
	return r, cancel, nil
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
