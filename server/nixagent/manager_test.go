package nixagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"main/tunnel"
)

type harness struct {
	t   *testing.T
	m   *Manager
	f   *Fake
	dir string

	mu     sync.Mutex
	status map[string]tunnel.NixStatus
	events []string
	locks  []tunnel.NixLock
	known  []string
	cancel context.CancelFunc
}

func newHarness(t *testing.T, dir string, f *Fake) *harness {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	if f == nil {
		f = NewFake()
	}
	cfg := Config{Dir: filepath.Join(dir, "agent"), EnvDir: filepath.Join(dir, "env"), StateRoot: filepath.Join(dir, "state"),
		Runner: f, Logf: func(string, ...any) {}, MonitorEvery: 15 * time.Millisecond, StartGrace: 10 * time.Millisecond}
	_ = os.MkdirAll(cfg.EnvDir, 0o700)
	m := NewManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, m: m, f: f, dir: dir, status: map[string]tunnel.NixStatus{}, cancel: cancel}
	s := m.subscribe()
	go func() {
		for mm := range s.ch {
			h.mu.Lock()
			switch mm.Type {
			case "status":
				for id, st := range mm.Status {
					h.status[id] = st
					if st.Event != "" {
						h.events = append(h.events, id+":"+st.Event)
					}
				}
			case "lock":
				h.locks = append(h.locks, *mm.Lock)
			case "known":
				h.known = mm.Known
			}
			h.mu.Unlock()
		}
	}()
	return h
}

func (h *harness) wait(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.t.Fatalf("timed out waiting for %s; status %+v events %v", what, h.status, h.events)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) phase(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status[id].Phase
}

func (h *harness) st(id string) tunnel.NixStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status[id]
}

func (h *harness) waitPhase(id, phase string) {
	h.t.Helper()
	h.wait(id+" "+phase, func() bool { return h.phase(id) == phase })
}

func (h *harness) hasEvent(e string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, x := range h.events {
		if x == e {
			return true
		}
	}
	return false
}

func svcSpec(id, slug string) tunnel.NixService {
	return tunnel.NixService{ID: id, Slug: slug, Run: tunnel.NixRunPackage, Attr: "default", Restart: "always",
		RemoveState: true, Desired: tunnel.NixRunning, Files: map[string]string{"flake.nix": "{ outputs = _: {}; } # " + slug}}
}

func TestManagerLifecycle(t *testing.T) {
	h := newHarness(t, "", nil)
	sp := svcSpec("a1", "kuma")
	h.m.Set([]tunnel.NixService{sp})
	h.waitPhase("a1", tunnel.PhaseRunning)
	if !h.hasEvent("a1:build-ok") || !h.hasEvent("a1:started") {
		t.Fatalf("events %v", h.events)
	}
	if !h.f.Active("kuma") || !strings.Contains(h.f.Unit("kuma"), "ExecStart=") {
		t.Fatalf("unit: %q", h.f.Unit("kuma"))
	}
	st := h.st("a1")
	if !strings.HasPrefix(st.Serving, "/nix/store/") || st.BuildHash == "" {
		t.Fatalf("status %+v", st)
	}

	// The relay locked the flake; the server echoes the lock back.
	h.wait("a lock", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.locks) == 1 })
	if h.locks[0].Base != "" || h.locks[0].ID != "a1" {
		t.Fatalf("lock %+v", h.locks[0])
	}
	builds, starts := h.f.BuildCount(), h.f.StartCount("kuma")
	sp.Lock = h.locks[0].Lock
	h.m.Set([]tunnel.NixService{sp})
	time.Sleep(80 * time.Millisecond)
	if h.f.BuildCount() != builds || h.f.StartCount("kuma") != starts {
		t.Fatalf("the echoed lock rebuilt (%d→%d) or restarted (%d→%d)", builds, h.f.BuildCount(), starts, h.f.StartCount("kuma"))
	}
	if st.BuildHash == "" || h.st("a1").BuildHash != sp.BuildHash() {
		t.Fatalf("build hash %q, spec's %q", h.st("a1").BuildHash, sp.BuildHash())
	}

	t.Run("env change restarts without a build", func(t *testing.T) {
		builds, starts := h.f.BuildCount(), h.f.StartCount("kuma")
		sp.Env = map[string]string{"PORT": "3001"}
		h.m.Set([]tunnel.NixService{sp})
		h.wait("restart", func() bool { return h.f.StartCount("kuma") == starts+1 })
		h.waitPhase("a1", tunnel.PhaseRunning)
		if h.f.BuildCount() != builds || !strings.Contains(h.f.Unit("kuma"), `Environment="PORT=3001"`) {
			t.Fatalf("builds %d→%d, unit %q", builds, h.f.BuildCount(), h.f.Unit("kuma"))
		}
	})

	t.Run("a lock change with the same output doesn't restart", func(t *testing.T) {
		builds, starts := h.f.BuildCount(), h.f.StartCount("kuma")
		sp.Lock = `{"nodes":{"root":{}},"root":"root","version":7,"x":1}`
		h.m.Set([]tunnel.NixService{sp})
		h.wait("rebuild", func() bool { return h.f.BuildCount() == builds+1 })
		h.waitPhase("a1", tunnel.PhaseRunning)
		time.Sleep(50 * time.Millisecond)
		if h.f.StartCount("kuma") != starts {
			t.Fatalf("restarted for the same output")
		}
	})

	var firstPath string
	t.Run("new files build and restart, keeping the previous", func(t *testing.T) {
		firstPath = h.st("a1").Serving
		starts := h.f.StartCount("kuma")
		sp.Files = map[string]string{"flake.nix": "{ outputs = _: {}; } # v2"}
		h.m.Set([]tunnel.NixService{sp})
		h.wait("new path", func() bool { s := h.st("a1"); return s.Serving != firstPath && s.Phase == tunnel.PhaseRunning })
		if h.st("a1").Previous != firstPath || h.f.StartCount("kuma") != starts+1 {
			t.Fatalf("status %+v, starts %d", h.st("a1"), h.f.StartCount("kuma")-starts)
		}
	})

	t.Run("a failed build keeps the old one running", func(t *testing.T) {
		serving := h.st("a1").Serving
		h.f.BuildErr = func(s tunnel.NixService) error {
			if strings.Contains(s.Files["flake.nix"], "broken") {
				return errors.New("attribute 'oops' missing")
			}
			return nil
		}
		sp.Files = map[string]string{"flake.nix": "broken"}
		h.m.Set([]tunnel.NixService{sp})
		h.waitPhase("a1", tunnel.PhaseBuildFailed)
		st := h.st("a1")
		if st.Serving != serving || !h.f.Active("kuma") || !strings.Contains(st.Error, "oops") || !strings.Contains(st.Log, "oops") {
			t.Fatalf("status %+v", st)
		}
		// It isn't retried until something changes.
		builds := h.f.BuildCount()
		time.Sleep(60 * time.Millisecond)
		if h.f.BuildCount() != builds {
			t.Fatal("retried a failed build on its own")
		}
		h.m.Action(tunnel.NixAction{ID: "a1", Action: tunnel.NixRebuild})
		h.wait("rebuild", func() bool { return h.f.BuildCount() == builds+1 })
	})

	t.Run("rollback", func(t *testing.T) {
		h.f.BuildErr = nil
		sp.Files = map[string]string{"flake.nix": "{ outputs = _: {}; } # v3"}
		h.m.Set([]tunnel.NixService{sp})
		h.wait("v3", func() bool {
			s := h.st("a1")
			return s.Phase == tunnel.PhaseRunning && s.Previous != "" && !strings.Contains(s.Serving, firstPath)
		})
		before := h.st("a1")
		h.m.Action(tunnel.NixAction{ID: "a1", Action: tunnel.NixRollback})
		h.wait("rolled back", func() bool { return h.st("a1").Serving == before.Previous && h.phase("a1") == tunnel.PhaseRunning })
		builds := h.f.BuildCount()
		time.Sleep(60 * time.Millisecond)
		if h.f.BuildCount() != builds {
			t.Fatal("rebuilt right after a rollback")
		}
	})

	t.Run("crash and crash loop", func(t *testing.T) {
		h.f.Crash("kuma", 1)
		h.waitPhase("a1", tunnel.PhaseCrashed)
		if h.st("a1").ExitCode == nil || !strings.Contains(h.st("a1").Log, "journal") {
			t.Fatalf("crash status %+v", h.st("a1"))
		}
		h.f.Crash("kuma", 6)
		h.waitPhase("a1", tunnel.PhaseCrashLoop)
	})

	t.Run("stop and start", func(t *testing.T) {
		sp.Desired = tunnel.NixStopped
		h.m.Set([]tunnel.NixService{sp})
		h.waitPhase("a1", tunnel.PhaseStopped)
		if h.f.Active("kuma") {
			t.Fatal("still running")
		}
		sp.Desired = tunnel.NixRunning
		h.m.Set([]tunnel.NixService{sp})
		h.waitPhase("a1", tunnel.PhaseRunning)
	})

	t.Run("teardown", func(t *testing.T) {
		state := filepath.Join(h.dir, "state", "kuma")
		_ = os.MkdirAll(state, 0o700)
		h.m.Set(nil)
		h.waitPhase("a1", tunnel.PhaseRemoved)
		if h.f.Unit("kuma") != "" {
			t.Fatal("unit left behind")
		}
		for _, p := range []string{state, filepath.Join(h.dir, "agent", "gcroots", "a1"), filepath.Join(h.dir, "agent", "src", "a1")} {
			if _, err := os.Stat(p); err == nil {
				t.Fatalf("%s left behind", p)
			}
		}
		h.wait("known empty", func() bool {
			h.m.snapshotTo(nil)
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.known != nil && len(h.known) == 0
		})
	})
}

func TestManagerMissingEnv(t *testing.T) {
	h := newHarness(t, "", nil)
	sp := svcSpec("e1", "db")
	sp.RelayEnv = []string{"DB_PASSWORD", "SMTP_PASS"}
	h.m.Set([]tunnel.NixService{sp})
	h.waitPhase("e1", tunnel.PhaseMissingEnv)
	if got := h.st("e1").MissingEnv; len(got) != 2 {
		t.Fatalf("missing %v", got)
	}
	if h.f.Active("db") {
		t.Fatal("started without its env")
	}
	_ = os.WriteFile(filepath.Join(h.dir, "env", "db.env"), []byte("DB_PASSWORD=x\nexport SMTP_PASS=y\n"), 0o600)
	h.waitPhase("e1", tunnel.PhaseRunning)
	if !strings.Contains(h.f.Unit("db"), "EnvironmentFile=-"+filepath.Join(h.dir, "env", "db.env")) {
		t.Fatalf("unit %q", h.f.Unit("db"))
	}
}

func TestManagerStartFailedAndRestore(t *testing.T) {
	dir := t.TempDir()
	f := NewFake()
	h := newHarness(t, dir, f)
	f.FailStarts("web", true)
	h.m.Set([]tunnel.NixService{svcSpec("w1", "web")})
	h.waitPhase("w1", tunnel.PhaseStartFailed)
	f.FailStarts("web", false)
	h.m.Action(tunnel.NixAction{ID: "w1", Action: tunnel.NixRestart})
	h.waitPhase("w1", tunnel.PhaseRunning)

	// The agent restarts after a reboot: the unit (in /run) is gone, and
	// comes back from state.json without the relay.
	h.cancel()
	time.Sleep(30 * time.Millisecond)
	f2 := NewFake()
	h2 := newHarness(t, dir, f2)
	h2.waitPhase("w1", tunnel.PhaseRunning)
	if !f2.Active("web") || f2.BuildCount() != 0 {
		t.Fatalf("restored: active %v, builds %d", f2.Active("web"), f2.BuildCount())
	}
}

func TestManagerSameSlugWaitsForTeardown(t *testing.T) {
	h := newHarness(t, "", nil)
	h.m.Set([]tunnel.NixService{svcSpec("old", "app")})
	h.waitPhase("old", tunnel.PhaseRunning)
	// Removed and applied again (a new ID, same slug) in one set.
	h.m.Set([]tunnel.NixService{svcSpec("new", "app")})
	h.waitPhase("old", tunnel.PhaseRemoved)
	h.waitPhase("new", tunnel.PhaseRunning)
	if !h.f.Active("app") {
		t.Fatal("the new one isn't running")
	}
}

func TestUnitFile(t *testing.T) {
	sp := svcSpec("u", "kuma")
	sp.Env = map[string]string{"B": `say "hi" 100% $HOME`, "A": "1"}
	sp.Restart = "on-failure"
	u := unitFile(sp, []string{"/nix/store/x-kuma/bin/kuma", "--port", "3001", "$HOME"}, "", "/etc/supermatrix/nix-services/kuma.env")
	for _, want := range []string{
		`ExecStart="/nix/store/x-kuma/bin/kuma" "--port" "3001" "$$HOME"`,
		`Environment="A=1"`,
		// systemd doesn't expand $ in Environment=: it's left alone.
		`Environment="B=say \"hi\" 100%% $HOME"`,
		`EnvironmentFile=-/etc/supermatrix/nix-services/kuma.env`,
		`StateDirectory=smx/kuma`,
		`WorkingDirectory=%S/smx/kuma`,
		`Restart=on-failure`,
	} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %s in\n%s", want, u)
		}
	}
	// A dev shell's directory is a plain path: systemd doesn't unquote it.
	if d := unitFile(sp, []string{"/bin/sh"}, "/var/lib/supermatrix-nix/src/x/ab", "/e"); !strings.Contains(d, "\nWorkingDirectory=/var/lib/supermatrix-nix/src/x/ab\n") {
		t.Errorf("working directory:\n%s", d)
	}
	if strings.Index(u, `Environment="A=1"`) > strings.Index(u, `Environment="B=`) {
		t.Error("env isn't sorted")
	}
}

func TestProgressParser(t *testing.T) {
	var log strings.Builder
	var last tunnel.NixProgress
	p := newProgress(&log, func(np tunnel.NixProgress) { last = np })
	in := strings.Join([]string{
		`@nix {"action":"start","id":1,"level":4,"parent":0,"text":"evaluating derivation 'path:/x#default'","type":0}`,
		`@nix {"action":"msg","level":3,"msg":"this derivation will be built:"}`,
		`@nix {"action":"start","id":2,"level":0,"parent":0,"text":"","type":104}`,
		`@nix {"action":"result","fields":[1,3,1,0],"id":2,"type":105}`,
		`@nix {"action":"start","id":3,"level":3,"parent":0,"text":"building '/nix/store/6f1pin7ri4f45r4hcbf6x9amhrw14x62-nodejs-20.11.1.drv'","type":105,"fields":["/nix/store/6f1pin7ri4f45r4hcbf6x9amhrw14x62-nodejs-20.11.1.drv","",1,1]}`,
		`@nix {"action":"result","fields":["compiling foo.c"],"id":3,"type":101}`,
		`@nix {"action":"msg","level":0,"msg":"\u001b[31;1merror:\u001b[0m builder for 'x' failed"}`,
		"",
	}, "\n")
	_, _ = p.Write([]byte(in))
	if last.Step != "building" || last.Built != 1 || last.ToBuild != 3 || last.Current != "nodejs-20.11.1" {
		t.Fatalf("progress %+v", last)
	}
	if !strings.Contains(log.String(), "compiling foo.c") || !strings.Contains(log.String(), "this derivation will be built") {
		t.Fatalf("log %q", log.String())
	}
	if p.err() != "builder for 'x' failed" {
		t.Fatalf("err %q", p.err())
	}
}

func TestManagerRespectsRestartNo(t *testing.T) {
	h := newHarness(t, "", nil)
	sp := svcSpec("n1", "job")
	sp.Restart = "no"
	h.m.Set([]tunnel.NixService{sp})
	h.waitPhase("n1", tunnel.PhaseRunning)
	starts := h.f.StartCount("job")
	h.f.Exit("job", 3)
	h.waitPhase("n1", tunnel.PhaseCrashed)
	time.Sleep(80 * time.Millisecond)
	if h.f.StartCount("job") != starts {
		t.Fatal("the agent restarted a service whose restart policy is no")
	}
	// Asking for it is another matter.
	h.m.Action(tunnel.NixAction{ID: "n1", Action: tunnel.NixRestart})
	h.waitPhase("n1", tunnel.PhaseRunning)
}

func TestUnsubscribeEndsTheWriter(t *testing.T) {
	h := newHarness(t, "", nil)
	s := h.m.subscribe()
	done := make(chan struct{})
	go func() {
		for range s.ch {
		}
		close(done)
	}()
	h.m.unsubscribe(s)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a relay that went away left its writer behind")
	}
	h.m.unsubscribe(s) // twice is fine
	h.m.emit(msg{Type: "known"})
}

func TestRebuildKeepsTheRunningDevShell(t *testing.T) {
	h := newHarness(t, "", nil)
	sp := svcSpec("d1", "shell")
	sp.Run = tunnel.NixRunDevShell
	sp.Command = []string{"npm", "start"}
	h.m.Set([]tunnel.NixService{sp})
	h.waitPhase("d1", tunnel.PhaseRunning)
	h.m.mu.Lock()
	cur := *h.m.svcs["d1"].rec.Current
	h.m.mu.Unlock()
	if _, err := os.Stat(cur.EnvFile); err != nil {
		t.Fatal(err)
	}
	// Same files, a rebuild that fails: what runs must keep its directory.
	h.f.BuildErr = func(tunnel.NixService) error { return errors.New("no network") }
	h.m.Action(tunnel.NixAction{ID: "d1", Action: tunnel.NixRebuild})
	h.waitPhase("d1", tunnel.PhaseBuildFailed)
	for _, p := range []string{cur.EnvFile, filepath.Join(cur.FilesDir, "flake.nix")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the running dev shell lost %s: %v", p, err)
		}
	}
}

func TestTeardownSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	f := NewFake()
	h := newHarness(t, dir, f)
	h.m.Set([]tunnel.NixService{svcSpec("t1", "gone")})
	h.waitPhase("t1", tunnel.PhaseRunning)
	// The relay removes it, and the agent stops before tearing it down.
	h.cancel()
	time.Sleep(30 * time.Millisecond)
	h.m.mu.Lock()
	s := h.m.svcs["t1"]
	h.m.mu.Unlock()
	s.mu.Lock()
	s.removed = true
	s.rec.Removing = true
	s.mu.Unlock()
	h.m.save()

	// Back up, with no relay to send a set: it finishes the teardown
	// instead of starting the service again.
	f2 := NewFake()
	_ = f2.InstallUnit("gone", "x")
	h2 := newHarness(t, dir, f2)
	h2.waitPhase("t1", tunnel.PhaseRemoved)
	if f2.StartCount("gone") != 0 || f2.Unit("gone") != "" {
		t.Fatalf("starts %d, unit %q", f2.StartCount("gone"), f2.Unit("gone"))
	}
}
