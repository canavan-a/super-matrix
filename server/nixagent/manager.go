package nixagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"main/tunnel"
)

// Config is where the agent keeps things and how it runs them.
type Config struct {
	// Dir is the agent's own state: flakes, GC roots, build logs and
	// state.json (/var/lib/supermatrix-nix).
	Dir string
	// EnvDir holds each service's <slug>.env, which the machine's owner
	// writes (/etc/supermatrix/nix-services).
	EnvDir string
	// StateRoot is where services' state directories are
	// (/var/lib/smx; systemd's StateDirectory=smx/<slug>).
	StateRoot string
	Runner    Runner
	Logf      func(format string, args ...any)
	// HostEvery is how often the host is checked again (default 5m),
	// MonitorEvery how often running services are looked at (5s), and
	// StartGrace how long a service must stay up to count as started (5s).
	HostEvery    time.Duration
	MonitorEvery time.Duration
	StartGrace   time.Duration
}

const (
	crashWindow    = 2 * time.Minute
	crashLoopAfter = 5
	keepBuildLogs  = 5
	logTailLines   = 200
)

// Manager runs the agent's services.
type Manager struct {
	cfg Config

	mu   sync.Mutex
	svcs map[string]*svc
	host *tunnel.NixHost
	subs map[*sub]struct{}
	ctx  context.Context

	saveMu sync.Mutex
}

type sub struct {
	ch     chan msg
	closed bool
}

func NewManager(cfg Config) *Manager {
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.HostEvery == 0 {
		cfg.HostEvery = 5 * time.Minute
	}
	if cfg.MonitorEvery == 0 {
		cfg.MonitorEvery = 5 * time.Second
	}
	if cfg.StartGrace == 0 {
		cfg.StartGrace = 5 * time.Second
	}
	return &Manager{cfg: cfg, svcs: map[string]*svc{}, subs: map[*sub]struct{}{}}
}

// build is one build of a service: what runs, from where, and the GC
// root that keeps it.
type build struct {
	Path string   `json:"path"`
	Exec []string `json:"exec"`
	// WorkDir is the unit's working directory ("" for its state directory).
	WorkDir string `json:"workDir"`
	// FilesDir is where its flake was built; EnvFile a devshell's
	// environment; Link its GC root.
	FilesDir string `json:"filesDir"`
	EnvFile  string `json:"envFile,omitempty"`
	Link     string `json:"link"`
	// Hash is the BuildHash it was built from (with the lock it ended up
	// with).
	Hash string `json:"hash"`
}

// record is what state.json keeps per service.
type record struct {
	Spec tunnel.NixService `json:"spec"`
	// Removing: it was being torn down; an agent that restarts finishes
	// that rather than starting it again.
	Removing bool   `json:"removing,omitempty"`
	Current  *build `json:"current,omitempty"`
	Previous *build `json:"previous,omitempty"`
	// PinnedAt is the spec's BuildHash when it was rolled back: the
	// previous build stays until the spec moves on.
	PinnedAt string `json:"pinnedAt,omitempty"`
	// UnitSlug and UnitHash are the unit as installed ("" for none).
	UnitSlug string `json:"unitSlug,omitempty"`
	UnitHash string `json:"unitHash,omitempty"`
}

type stateFile struct {
	Services map[string]record `json:"services"`
}

func (m *Manager) statePath() string { return filepath.Join(m.cfg.Dir, "state.json") }

// Start checks the host, loads the saved services and starts them, then
// checks the host again every HostEvery until ctx ends. Services keep
// running (they're systemd's) when the agent stops.
func (m *Manager) Start(ctx context.Context) error {
	for _, d := range []string{m.cfg.Dir, filepath.Join(m.cfg.Dir, "src"), filepath.Join(m.cfg.Dir, "gcroots"), filepath.Join(m.cfg.Dir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.checkHost(ctx)
	var st stateFile
	if b, err := os.ReadFile(m.statePath()); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			m.cfg.Logf("nix agent: %s is damaged (%v); starting empty", m.statePath(), err)
		}
	}
	m.mu.Lock()
	for id, rec := range st.Services {
		s := m.newSvc(id, rec)
		s.removed = rec.Removing
		m.svcs[id] = s
		go s.run()
	}
	m.mu.Unlock()
	if len(st.Services) > 0 {
		m.cfg.Logf("nix agent: %d service(s) from %s", len(st.Services), m.statePath())
	}
	go func() {
		t := time.NewTicker(m.cfg.HostEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.checkHost(ctx)
			}
		}
	}()
	return nil
}

func (m *Manager) checkHost(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	h := m.cfg.Runner.Host(cctx)
	h.CheckedAt = time.Now().UTC().Truncate(time.Second)
	m.mu.Lock()
	changed := m.host == nil || m.host.OK != h.OK || m.host.Error != h.Error || m.host.Version != h.Version || m.host.System != h.System
	m.host = &h
	m.mu.Unlock()
	if changed {
		if h.OK {
			m.cfg.Logf("nix agent: host ok (nix %s, %s)", h.Version, h.System)
		} else {
			m.cfg.Logf("nix agent: this machine can't run Nix services: %s", h.Error)
		}
		m.emit(msg{Type: "host", Host: &h})
	}
}

// Host is the last host check.
func (m *Manager) Host() *tunnel.NixHost {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host == nil {
		return nil
	}
	h := *m.host
	return &h
}

func (m *Manager) system() string {
	if h := m.Host(); h != nil {
		return h.System
	}
	return ""
}

// subscribe adds a relay connection to report to; it gets the host check,
// every service's status and what's known first.
func (m *Manager) subscribe() *sub {
	s := &sub{ch: make(chan msg, 4096)}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	if m.host != nil {
		h := *m.host
		s.ch <- msg{Type: "host", Host: &h}
	}
	m.mu.Unlock()
	m.snapshotTo(s)
	return s
}

func (m *Manager) unsubscribe(s *sub) {
	m.mu.Lock()
	delete(m.subs, s)
	// Ends its writer (sendTo closes it already when it fell behind).
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	m.mu.Unlock()
}

// snapshotTo sends every service's status and the known list to s (nil:
// everyone).
func (m *Manager) snapshotTo(s *sub) {
	m.mu.Lock()
	all := map[string]tunnel.NixStatus{}
	known := make([]string, 0, len(m.svcs))
	for id, sv := range m.svcs {
		sv.mu.Lock()
		all[id] = sv.st
		sv.mu.Unlock()
		known = append(known, id)
	}
	m.mu.Unlock()
	sort.Strings(known)
	msgs := []msg{}
	if len(all) > 0 {
		msgs = append(msgs, msg{Type: "status", Status: all})
	}
	msgs = append(msgs, msg{Type: "known", Known: known})
	for _, mm := range msgs {
		if s != nil {
			m.mu.Lock()
			m.sendTo(s, mm)
			m.mu.Unlock()
		} else {
			m.emit(mm)
		}
	}
}

// sendTo queues mm for s; m.mu is held. A relay that falls this far
// behind is dropped (it reconnects and gets a fresh snapshot).
func (m *Manager) sendTo(s *sub, mm msg) {
	if s.closed {
		return
	}
	select {
	case s.ch <- mm:
	default:
		s.closed = true
		close(s.ch)
	}
}

func (m *Manager) emit(mm msg) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		m.sendTo(s, mm)
	}
}

// Set makes the agent's services exactly set: new ones are built and
// started, changed ones updated, missing ones torn down.
func (m *Manager) Set(set []tunnel.NixService) {
	m.mu.Lock()
	want := map[string]tunnel.NixService{}
	for _, sp := range set {
		want[sp.ID] = sp
	}
	removing := map[string]*svc{} // slug → a service being torn down
	for id, s := range m.svcs {
		if _, ok := want[id]; !ok {
			s.remove()
		}
		if s.isRemoved() {
			removing[s.slug()] = s
		}
	}
	for _, sp := range set {
		if s, ok := m.svcs[sp.ID]; ok && !s.isRemoved() {
			s.update(sp)
			continue
		}
		if _, ok := m.svcs[sp.ID]; ok {
			continue // being torn down; it's applied again under a new ID
		}
		s := m.newSvc(sp.ID, record{Spec: sp})
		if old := removing[sp.Slug]; old != nil {
			s.after = old.done // the same unit name: let the old one go first
		}
		m.svcs[sp.ID] = s
		if m.ctx != nil {
			go s.run()
		}
	}
	m.mu.Unlock()
	m.save()
	m.snapshotTo(nil)
}

// Action restarts, rebuilds or rolls back one service.
func (m *Manager) Action(act tunnel.NixAction) {
	m.mu.Lock()
	s := m.svcs[act.ID]
	m.mu.Unlock()
	if s == nil || s.isRemoved() {
		return
	}
	select {
	case s.actions <- act.Action:
	default:
	}
}

// Logs is a service's logs: the newest build log (req.Build), or its
// journal.
func (m *Manager) Logs(ctx context.Context, id string, req tunnel.NixLogs) (io.ReadCloser, error) {
	m.mu.Lock()
	s := m.svcs[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("no such Nix service on this relay")
	}
	if req.Build {
		logs, _ := filepath.Glob(filepath.Join(m.cfg.Dir, "logs", id, "build-*.log"))
		if len(logs) == 0 {
			return nil, errors.New("it hasn't been built here yet")
		}
		sort.Strings(logs)
		return os.Open(logs[len(logs)-1])
	}
	lines := req.Lines
	if lines <= 0 {
		lines = 200
	}
	return m.cfg.Runner.Journal(ctx, s.unitSlug(), lines, req.Follow)
}

func (m *Manager) forget(id string, s *svc) {
	m.mu.Lock()
	if m.svcs[id] == s {
		delete(m.svcs, id)
	}
	m.mu.Unlock()
	m.save()
}

// save writes state.json (atomically).
func (m *Manager) save() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	st := stateFile{Services: map[string]record{}}
	m.mu.Lock()
	for id, s := range m.svcs {
		s.mu.Lock()
		if !s.removed {
			st.Services[id] = s.rec
		} else if s.rec.UnitHash != "" || s.rec.Current != nil {
			// Still being torn down: after a crash the agent finishes it,
			// since the next set won't list it either.
			st.Services[id] = s.rec
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := m.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		m.cfg.Logf("nix agent: saving state: %v", err)
		return
	}
	if err := os.Rename(tmp, m.statePath()); err != nil {
		m.cfg.Logf("nix agent: saving state: %v", err)
	}
}

// --- one service ---

type svc struct {
	m  *Manager
	id string

	mu      sync.Mutex
	rec     record
	st      tunnel.NixStatus
	removed bool

	kick    chan struct{}
	actions chan string
	// done is closed once the service's goroutine ends (after its
	// teardown, when it's removed); after, if set, is waited for first.
	done  chan struct{}
	after <-chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	buildCancel context.CancelFunc
	building    string
	failedHash  string
	startFailed string
	// startedFor is the unit hash last started successfully: once it
	// stops by itself, systemd's restart policy decides, not the agent.
	startedFor    string
	forceRebuild  bool
	restartWanted bool
	lastRestarts  int
	restartTimes  []time.Time
	lastProgress  time.Time
}

func (m *Manager) newSvc(id string, rec record) *svc {
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &svc{m: m, id: id, rec: rec, kick: make(chan struct{}, 1), actions: make(chan string, 8),
		done: make(chan struct{}), ctx: ctx, cancel: cancel,
		st: tunnel.NixStatus{Phase: tunnel.PhasePending, Since: time.Now().UTC()}}
}

func (s *svc) spec() tunnel.NixService {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Spec
}

func (s *svc) slug() string { return s.spec().Slug }

// unitSlug is the slug the unit is installed under.
func (s *svc) unitSlug() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec.UnitSlug != "" {
		return s.rec.UnitSlug
	}
	return s.rec.Spec.Slug
}

func (s *svc) isRemoved() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removed
}

func (s *svc) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *svc) update(sp tunnel.NixService) {
	s.mu.Lock()
	if s.buildCancel != nil && s.building != "" && sp.BuildHash() != s.building {
		s.buildCancel() // a newer flake: build that instead
	}
	s.rec.Spec = sp
	s.mu.Unlock()
	s.poke()
}

func (s *svc) remove() {
	s.mu.Lock()
	if s.removed {
		s.mu.Unlock()
		return
	}
	s.removed = true
	s.rec.Removing = true
	if s.buildCancel != nil {
		s.buildCancel()
	}
	s.mu.Unlock()
	s.poke()
}

// set moves the service to phase (or updates it in place) and reports
// it; event, if any, says what just happened.
func (s *svc) set(phase, event string, f func(st *tunnel.NixStatus)) {
	s.mu.Lock()
	st := s.st
	if phase != st.Phase {
		st.Since = time.Now().UTC()
		st.Error, st.Log, st.MissingEnv, st.ExitCode, st.Teardown, st.Progress = "", "", nil, nil, nil, nil
		if phase != tunnel.PhaseCrashed && phase != tunnel.PhaseCrashLoop {
			st.Restarts = 0
		}
	}
	st.Phase = phase
	st.Serving, st.Previous, st.BuildHash = "", "", ""
	if s.rec.Current != nil {
		st.Serving, st.BuildHash = s.rec.Current.Path, s.rec.Current.Hash
	}
	if s.rec.Previous != nil {
		st.Previous = s.rec.Previous.Path
	}
	if f != nil {
		f(&st)
	}
	st.Event = ""
	s.st = st
	s.mu.Unlock()
	st.Event = event
	s.m.emit(msg{Type: "status", Status: map[string]tunnel.NixStatus{s.id: st}})
}

func (s *svc) phase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Phase
}

func (s *svc) run() {
	defer close(s.done)
	if s.after != nil {
		select {
		case <-s.after:
		case <-s.ctx.Done():
			return
		}
	}
	s.boot()
	tick := time.NewTicker(s.m.cfg.MonitorEvery)
	defer tick.Stop()
	for {
		if s.isRemoved() {
			s.teardown()
			s.m.forget(s.id, s)
			return
		}
		s.reconcile()
		select {
		case <-s.ctx.Done():
			if s.isRemoved() {
				continue
			}
			return
		case <-s.kick:
		case act := <-s.actions:
			s.action(act)
		case <-tick.C:
			s.monitor()
		}
	}
}

// boot notices a unit that's gone (units in /run don't survive a reboot),
// so reconcile installs it again.
func (s *svc) boot() {
	s.mu.Lock()
	installed := s.rec.UnitHash != ""
	slug := s.rec.UnitSlug
	s.mu.Unlock()
	if !installed {
		return
	}
	st, err := s.m.cfg.Runner.UnitState(s.ctx, slug)
	if err != nil || !st.Loaded {
		s.mu.Lock()
		s.rec.UnitHash = ""
		s.mu.Unlock()
	}
}

func (s *svc) action(act string) {
	s.mu.Lock()
	switch act {
	case tunnel.NixRestart:
		s.restartWanted, s.startFailed = true, ""
	case tunnel.NixRebuild:
		s.forceRebuild, s.failedHash = true, ""
	case tunnel.NixRollback:
		if s.rec.Previous == nil {
			s.mu.Unlock()
			return
		}
		s.rec.Current, s.rec.Previous = s.rec.Previous, s.rec.Current
		s.rec.PinnedAt = s.rec.Spec.BuildHash()
		s.restartWanted, s.startFailed, s.failedHash = true, "", ""
	}
	s.mu.Unlock()
	if act == tunnel.NixRollback {
		s.m.save()
		s.set(s.phase(), "rolled-back", func(st *tunnel.NixStatus) { st.Error = "" })
	}
}

func (s *svc) envFile(slug string) string { return filepath.Join(s.m.cfg.EnvDir, slug+".env") }

// missingEnv is the relay_env names the relay's env file doesn't set.
func (s *svc) missingEnv(spec tunnel.NixService) []string {
	if len(spec.RelayEnv) == 0 {
		return nil
	}
	b, _ := os.ReadFile(s.envFile(spec.Slug))
	set := parseEnvFile(string(b))
	var missing []string
	for _, n := range spec.RelayEnv {
		if !set[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

// reconcile brings the service to what its spec asks: built, its unit
// installed, running or stopped.
func (s *svc) reconcile() {
	spec := s.spec()
	want := spec.BuildHash()
	s.mu.Lock()
	cur := s.rec.Current
	needBuild := cur == nil || (cur.Hash != want && s.rec.PinnedAt != want)
	force := s.forceRebuild
	s.forceRebuild = false
	skip := !force && s.failedHash == want
	s.mu.Unlock()

	if (needBuild || force) && !skip {
		if !s.build(spec) && s.ctx.Err() != nil {
			return
		}
	}
	s.mu.Lock()
	cur = s.rec.Current
	buildFailed := s.failedHash == want
	s.mu.Unlock()
	if s.isRemoved() {
		return
	}

	if spec.Desired == tunnel.NixStopped {
		s.stop(buildFailed)
		return
	}
	if missing := s.missingEnv(spec); len(missing) > 0 {
		if s.phase() != tunnel.PhaseMissingEnv || !slices.Equal(s.statusMissing(), missing) {
			s.set(tunnel.PhaseMissingEnv, tunnel.PhaseMissingEnv, func(st *tunnel.NixStatus) { st.MissingEnv = missing })
		}
		return
	}
	if cur == nil {
		return // nothing has built yet
	}

	content := unitFile(spec, cur.Exec, cur.WorkDir, s.envFile(spec.Slug))
	uh := hashString(content)
	s.mu.Lock()
	oldSlug, installed := s.rec.UnitSlug, s.rec.UnitHash
	restart := s.restartWanted
	startFailed, startedFor := s.startFailed, s.startedFor
	s.mu.Unlock()
	r := s.m.cfg.Runner
	if oldSlug != "" && oldSlug != spec.Slug {
		// Renamed: the old unit goes.
		_ = r.Systemctl(s.ctx, "stop", oldSlug)
		_ = r.RemoveUnit(oldSlug)
		installed = ""
	}
	if uh != installed {
		if err := r.InstallUnit(spec.Slug, content); err != nil {
			s.set(tunnel.PhaseStartFailed, tunnel.PhaseStartFailed, func(st *tunnel.NixStatus) { st.Error = "writing the unit: " + err.Error() })
			return
		}
		s.mu.Lock()
		s.rec.UnitSlug, s.rec.UnitHash = spec.Slug, uh
		s.mu.Unlock()
		s.m.save()
		restart = true
	}
	ust, err := r.UnitState(s.ctx, spec.Slug)
	active := err == nil && (ust.Active == "active" || ust.Active == "activating" || ust.Active == "reloading")
	switch {
	case restart || (!active && startFailed != uh && startedFor != uh):
		s.start(spec, uh, buildFailed)
	case !active:
		// It failed to start with this unit; wait for a change or restart.
	default:
		switch s.phase() {
		case tunnel.PhaseCrashed, tunnel.PhaseCrashLoop:
			// monitor decides when it's recovered
		default:
			if buildFailed {
				if s.phase() != tunnel.PhaseBuildFailed {
					s.set(tunnel.PhaseBuildFailed, "", nil)
				}
			} else if s.phase() != tunnel.PhaseRunning {
				s.set(tunnel.PhaseRunning, "", nil)
			}
		}
	}
}

func (s *svc) statusMissing() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.MissingEnv
}

// start (re)starts the unit and waits to see that it stays up.
func (s *svc) start(spec tunnel.NixService, uh string, buildFailed bool) {
	r := s.m.cfg.Runner
	s.set(tunnel.PhaseStarting, "", nil)
	s.mu.Lock()
	s.restartWanted = false
	s.mu.Unlock()
	if err := r.Systemctl(s.ctx, "restart", spec.Slug); err != nil {
		s.startFailedNow(spec, uh, err.Error())
		return
	}
	select {
	case <-time.After(s.m.cfg.StartGrace):
	case <-s.ctx.Done():
		return
	}
	st, err := r.UnitState(s.ctx, spec.Slug)
	if err != nil || st.Active != "active" || st.NRestarts > 0 {
		why := "it didn't stay up"
		if err == nil && st.Result != "" && st.Result != "success" {
			why = fmt.Sprintf("it exited (%s, status %d)", st.Result, st.ExitStatus)
		}
		s.startFailedNow(spec, uh, why)
		return
	}
	s.mu.Lock()
	s.startFailed, s.startedFor, s.lastRestarts, s.restartTimes = "", uh, 0, nil
	s.mu.Unlock()
	phase := tunnel.PhaseRunning
	if buildFailed {
		phase = tunnel.PhaseBuildFailed
	}
	s.set(phase, "started", nil)
}

func (s *svc) startFailedNow(spec tunnel.NixService, uh, why string) {
	tail := s.journalTail(spec.Slug, 100)
	st, _ := s.m.cfg.Runner.UnitState(s.ctx, spec.Slug)
	s.mu.Lock()
	s.startFailed = uh
	s.mu.Unlock()
	s.set(tunnel.PhaseStartFailed, tunnel.PhaseStartFailed, func(ns *tunnel.NixStatus) {
		ns.Error, ns.Log = why, tail
		code := st.ExitStatus
		ns.ExitCode = &code
	})
}

func (s *svc) stop(buildFailed bool) {
	s.mu.Lock()
	installed := s.rec.UnitHash != ""
	slug := s.rec.UnitSlug
	// Stopped on purpose: starting it again is the agent's job.
	s.startedFor = ""
	s.mu.Unlock()
	if s.phase() == tunnel.PhaseStopped {
		return
	}
	if installed {
		s.set(tunnel.PhaseStopping, "", nil)
		if err := s.m.cfg.Runner.Systemctl(s.ctx, "stop", slug); err != nil {
			s.set(tunnel.PhaseStopped, "stopped", func(st *tunnel.NixStatus) { st.Error = "stopping: " + err.Error() })
			return
		}
	}
	s.set(tunnel.PhaseStopped, "stopped", func(st *tunnel.NixStatus) {
		if buildFailed {
			st.Error = "its latest build failed"
		}
	})
}

// monitor looks at a running service: crashes, crash loops, recovery,
// and relay env that's been filled in since.
func (s *svc) monitor() {
	spec := s.spec()
	ph := s.phase()
	if ph == tunnel.PhaseMissingEnv {
		s.poke()
		return
	}
	s.mu.Lock()
	installed := s.rec.UnitHash != ""
	s.mu.Unlock()
	if !installed || spec.Desired == tunnel.NixStopped {
		return
	}
	switch ph {
	case tunnel.PhaseRunning, tunnel.PhaseCrashed, tunnel.PhaseCrashLoop, tunnel.PhaseBuildFailed:
	default:
		return
	}
	st, err := s.m.cfg.Runner.UnitState(s.ctx, spec.Slug)
	if err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	newRestarts := st.NRestarts - s.lastRestarts
	if newRestarts < 0 {
		newRestarts = st.NRestarts
	}
	s.lastRestarts = st.NRestarts
	for i := 0; i < newRestarts; i++ {
		s.restartTimes = append(s.restartTimes, now)
	}
	recent := s.restartTimes[:0]
	for _, t := range s.restartTimes {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	s.restartTimes = recent
	n := len(recent)
	s.mu.Unlock()

	crash := func(phase string) {
		tail := s.journalTail(spec.Slug, 100)
		s.set(phase, "crashed", func(ns *tunnel.NixStatus) {
			code := st.ExitStatus
			ns.ExitCode, ns.Restarts, ns.Log = &code, n, tail
			ns.Error = fmt.Sprintf("exited (%s, status %d)", st.Result, st.ExitStatus)
			if phase == tunnel.PhaseCrashLoop {
				ns.Error = fmt.Sprintf("restarted %d times in 2 minutes; last %s", n, ns.Error)
			}
		})
	}
	switch {
	case newRestarts > 0 && n > crashLoopAfter:
		crash(tunnel.PhaseCrashLoop)
	case newRestarts > 0:
		crash(tunnel.PhaseCrashed)
	case st.Active == "failed" && ph != tunnel.PhaseCrashed && ph != tunnel.PhaseCrashLoop:
		crash(tunnel.PhaseCrashed)
	case st.Active == "inactive" && ph == tunnel.PhaseRunning:
		s.set(tunnel.PhaseStopped, "exited", func(ns *tunnel.NixStatus) {
			ns.Error = fmt.Sprintf("it exited by itself (status %d) and restart is %s", st.ExitStatus, spec.Restart)
		})
	case (ph == tunnel.PhaseCrashed || ph == tunnel.PhaseCrashLoop) && st.Active == "active" && n == 0:
		s.set(tunnel.PhaseRunning, "recovered", nil)
	}
}

func (s *svc) journalTail(slug string, lines int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc, err := s.m.cfg.Runner.Journal(ctx, slug, lines, false)
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
	return string(b)
}

// tail keeps the last n lines written to it.
type tail struct {
	mu    sync.Mutex
	n     int
	lines []string
	part  string
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.part += string(p)
	for {
		i := strings.IndexByte(t.part, '\n')
		if i < 0 {
			break
		}
		t.lines = append(t.lines, t.part[:i])
		t.part = t.part[i+1:]
		if len(t.lines) > t.n {
			t.lines = t.lines[len(t.lines)-t.n:]
		}
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := strings.Join(t.lines, "\n")
	if t.part != "" {
		out += "\n" + t.part
	}
	return out
}

// writeFiles writes a service's flake into dir, which it empties first.
func writeFiles(dir string, spec tunnel.NixService) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string]string{}
	for p, c := range spec.Files {
		files[p] = c
	}
	if spec.Lock != "" {
		files["flake.lock"] = spec.Lock
	}
	for p, c := range files {
		clean := filepath.Clean(p)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("file %q is outside the flake", p)
		}
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// build builds spec; it reports false if it failed (or was cancelled).
func (s *svc) build(spec tunnel.NixService) bool {
	want := spec.BuildHash()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	s.mu.Lock()
	s.buildCancel, s.building = cancel, want
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.buildCancel, s.building = nil, ""
		s.mu.Unlock()
	}()
	fail := func(err error, log string) bool {
		if ctx.Err() != nil {
			return false // superseded or removed; reconcile runs again
		}
		s.mu.Lock()
		s.failedHash = want
		s.mu.Unlock()
		s.set(tunnel.PhaseBuildFailed, tunnel.PhaseBuildFailed, func(st *tunnel.NixStatus) {
			st.Error, st.Log = err.Error(), log
		})
		return false
	}

	s.set(tunnel.PhasePreparing, "", nil)
	dir := filepath.Join(s.m.cfg.Dir, "src", s.id, filesHash(spec)[:16])
	s.mu.Lock()
	inUse := (s.rec.Current != nil && s.rec.Current.FilesDir == dir) || (s.rec.Previous != nil && s.rec.Previous.FilesDir == dir)
	s.mu.Unlock()
	write := writeFiles
	if inUse {
		// The same files (the directory is named by them), and a build
		// still runs from it: only the lock can differ.
		write = writeLock
	}
	if err := write(dir, spec); err != nil {
		return fail(fmt.Errorf("writing the flake: %w", err), "")
	}
	system := s.m.system()
	if system == "" {
		return fail(errors.New("the host check hasn't found Nix's system yet"), "")
	}
	logDir := filepath.Join(s.m.cfg.Dir, "logs", s.id)
	_ = os.MkdirAll(logDir, 0o700)
	lf, err := os.Create(filepath.Join(logDir, fmt.Sprintf("build-%d.log", time.Now().UnixNano())))
	if err != nil {
		return fail(fmt.Errorf("opening the build log: %w", err), "")
	}
	defer lf.Close()
	pruneLogs(logDir)
	tl := &tail{n: logTailLines}
	linkDir := filepath.Join(s.m.cfg.Dir, "gcroots", s.id)
	_ = os.MkdirAll(linkDir, 0o700)
	job := BuildJob{
		Spec: spec, Dir: dir, System: system,
		OutLink: filepath.Join(linkDir, want[:16]),
		Log:     io.MultiWriter(lf, tl),
		Progress: func(p tunnel.NixProgress) {
			s.mu.Lock()
			due := time.Since(s.lastProgress) >= time.Second
			if due {
				s.lastProgress = time.Now()
			}
			s.mu.Unlock()
			if due {
				s.set(tunnel.PhaseBuilding, "", func(st *tunnel.NixStatus) { st.Progress = &p })
			}
		},
	}
	if spec.Run == tunnel.NixRunDevShell {
		job.EnvFile = filepath.Join(dir, ".smx-env-"+want[:16]+".sh")
	}
	s.set(tunnel.PhaseBuilding, "", func(st *tunnel.NixStatus) { st.Progress = &tunnel.NixProgress{Step: "evaluating"} })
	started := time.Now()
	res, err := s.m.cfg.Runner.Build(ctx, job)
	if err != nil {
		return fail(err, tl.String())
	}
	fmt.Fprintf(lf, "\nbuilt %s in %s\n", res.StorePath, time.Since(started).Round(time.Second))

	effective := spec
	if !spec.LockPinned {
		if b, err := os.ReadFile(filepath.Join(dir, "flake.lock")); err == nil && len(b) > 0 && string(b) != spec.Lock {
			effective.Lock = string(b)
			s.m.emit(msg{Type: "lock", Lock: &tunnel.NixLock{ID: s.id, Base: tunnel.HashLock(spec.Lock), Lock: effective.Lock}})
		}
	}
	nb := &build{Path: res.StorePath, Exec: res.Exec, FilesDir: dir, EnvFile: job.EnvFile, Link: job.OutLink, Hash: effective.BuildHash()}
	if spec.Run == tunnel.NixRunDevShell {
		nb.WorkDir = dir
	}
	s.mu.Lock()
	if effective.Lock != spec.Lock && s.rec.Spec.BuildHash() == want {
		// Take the lock as ours until the server says otherwise (it
		// sends it back, or another relay's): a restart in between
		// shouldn't rebuild.
		s.rec.Spec.Lock = effective.Lock
	}
	if s.rec.Current == nil || s.rec.Current.Path != nb.Path || !slices.Equal(s.rec.Current.Exec, nb.Exec) {
		s.rec.Previous = s.rec.Current
	}
	s.rec.Current = nb
	s.rec.PinnedAt = ""
	s.failedHash = ""
	keep := []*build{s.rec.Current, s.rec.Previous}
	s.mu.Unlock()
	s.prune(keep)
	s.m.save()
	s.set(tunnel.PhaseBuilding, "build-ok", func(st *tunnel.NixStatus) { st.Progress = nil })
	return true
}

// prune deletes GC roots and flake directories no kept build uses.
func (s *svc) prune(keep []*build) {
	keepLinks, keepDirs := map[string]bool{}, map[string]bool{}
	for _, b := range keep {
		if b != nil {
			keepLinks[b.Link], keepDirs[b.FilesDir] = true, true
		}
	}
	links, _ := filepath.Glob(filepath.Join(s.m.cfg.Dir, "gcroots", s.id, "*"))
	for _, l := range links {
		if !keepLinks[l] {
			_ = os.Remove(l)
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(s.m.cfg.Dir, "src", s.id, "*"))
	for _, d := range dirs {
		if !keepDirs[d] {
			_ = os.RemoveAll(d)
		}
	}
}

func pruneLogs(dir string) {
	logs, _ := filepath.Glob(filepath.Join(dir, "build-*.log"))
	sort.Strings(logs)
	for len(logs) >= keepBuildLogs {
		_ = os.Remove(logs[0])
		logs = logs[1:]
	}
}

// teardown removes the service from the machine, step by step, trying
// every step even when one fails.
func (s *svc) teardown() {
	s.set(tunnel.PhaseTearingDown, "", nil)
	spec := s.spec()
	slug := s.unitSlug()
	s.mu.Lock()
	installed := s.rec.UnitHash != ""
	s.mu.Unlock()
	r := s.m.cfg.Runner
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var failed []string
	step := func(name string, err error) {
		if err != nil {
			failed = append(failed, name+": "+err.Error())
		}
	}
	if installed {
		step("stop", r.Systemctl(ctx, "stop", slug))
		step("remove the unit", r.RemoveUnit(slug))
	}
	step("remove the GC roots", os.RemoveAll(filepath.Join(s.m.cfg.Dir, "gcroots", s.id)))
	step("remove the flake", os.RemoveAll(filepath.Join(s.m.cfg.Dir, "src", s.id)))
	step("remove the build logs", os.RemoveAll(filepath.Join(s.m.cfg.Dir, "logs", s.id)))
	if spec.RemoveState && s.m.cfg.StateRoot != "" && slug != "" {
		step("remove the state directory", os.RemoveAll(filepath.Join(s.m.cfg.StateRoot, slug)))
	}
	s.mu.Lock()
	s.rec.UnitHash, s.rec.Current, s.rec.Previous = "", nil, nil
	s.mu.Unlock()
	if len(failed) > 0 {
		s.m.cfg.Logf("nix agent: removing %s: %s", slug, strings.Join(failed, "; "))
		s.set(tunnel.PhaseRemovedWithErrors, tunnel.PhaseRemovedWithErrors, func(st *tunnel.NixStatus) { st.Teardown = failed })
		return
	}
	s.m.cfg.Logf("nix agent: removed %s", slug)
	s.set(tunnel.PhaseRemoved, tunnel.PhaseRemoved, nil)
}

// readLines is used by tests and the system runner to read text.
func readLines(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// writeLock brings dir's flake.lock to spec's (removing it for none),
// leaving the other files alone.
func writeLock(dir string, spec tunnel.NixService) error {
	p := filepath.Join(dir, "flake.lock")
	if spec.Lock == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, []byte(spec.Lock), 0o600)
}
