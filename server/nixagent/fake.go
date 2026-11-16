package nixagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"main/tunnel"
)

// Fake is a Runner for tests: builds take no time and make a store path
// from the flake's contents, units live in memory, and a test can make a
// build fail or a service crash.
type Fake struct {
	mu sync.Mutex
	// HostState is what Host reports (OK, x86_64-linux by default).
	HostState tunnel.NixHost
	// BuildErr, when set, decides whether a build fails.
	BuildErr func(spec tunnel.NixService) error
	// BuildDelay makes each build take this long.
	BuildDelay time.Duration
	// Units is each installed unit's content, by slug.
	Units  map[string]string
	states map[string]*UnitState
	// Builds counts builds; Starts (re)starts, by slug.
	Builds int
	Starts map[string]int
	// failStart: the next starts of a slug exit at once.
	failStart map[string]bool
}

func NewFake() *Fake {
	return &Fake{
		HostState: tunnel.NixHost{OK: true, Version: "2.24.0", Flakes: true, Daemon: true, Systemd: true, System: "x86_64-linux"},
		Units:     map[string]string{}, states: map[string]*UnitState{}, Starts: map[string]int{}, failStart: map[string]bool{},
	}
}

func (f *Fake) Host(context.Context) tunnel.NixHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.HostState
}

// SetHost changes what Host reports.
func (f *Fake) SetHost(h tunnel.NixHost) {
	f.mu.Lock()
	f.HostState = h
	f.mu.Unlock()
}

func (f *Fake) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
	f.mu.Lock()
	errf, delay := f.BuildErr, f.BuildDelay
	f.Builds++
	f.mu.Unlock()
	fmt.Fprintf(job.Log, "building %s (fake)\n", job.Spec.Slug)
	if job.Progress != nil {
		job.Progress(tunnel.NixProgress{Step: "building", Built: 0, ToBuild: 1, Current: job.Spec.Slug})
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return BuildResult{}, ctx.Err()
		}
	}
	if errf != nil {
		if err := errf(job.Spec); err != nil {
			fmt.Fprintf(job.Log, "error: %v\n", err)
			return BuildResult{}, err
		}
	}
	// Like nix, make a lock when there's none (pinned ones are left).
	lock := filepath.Join(job.Dir, "flake.lock")
	if _, err := os.Stat(lock); err != nil {
		_ = os.WriteFile(lock, []byte(`{"nodes":{"root":{}},"root":"root","version":7}`), 0o600)
	}
	// The output depends on the files (not the lock), as a build would.
	h := sha256.New()
	fmt.Fprint(h, job.Spec.Run, job.Spec.Attr, filesHash(job.Spec))
	sum := hex.EncodeToString(h.Sum(nil))[:32]
	path := "/nix/store/" + sum + "-" + job.Spec.Slug
	_ = os.Remove(job.OutLink)
	if err := os.Symlink(path, job.OutLink); err != nil {
		return BuildResult{}, err
	}
	exec := append([]string{path + "/bin/" + job.Spec.Slug}, job.Spec.Command...)
	if job.Spec.Run == tunnel.NixRunDevShell {
		_ = os.WriteFile(job.EnvFile, []byte("export FAKE=1\n"), 0o600)
		exec = append([]string{"/bin/bash", "-c", `. "$1"; shift; exec "$@"`, "smx", job.EnvFile}, job.Spec.Command...)
	}
	fmt.Fprintf(job.Log, "store path: %s\n", path)
	return BuildResult{StorePath: path, Exec: exec}, nil
}

func (f *Fake) InstallUnit(slug, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Units[slug] = content
	if f.states[slug] == nil {
		f.states[slug] = &UnitState{Active: "inactive", Loaded: true}
	}
	return nil
}

func (f *Fake) RemoveUnit(slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Units, slug)
	delete(f.states, slug)
	return nil
}

func (f *Fake) Systemctl(_ context.Context, verb, slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.states[slug]
	if st == nil {
		return errors.New("Unit " + UnitName(slug) + " not found.")
	}
	switch verb {
	case "start", "restart":
		f.Starts[slug]++
		st.NRestarts = 0
		if f.failStart[slug] {
			st.Active, st.Result, st.ExitStatus = "failed", "exit-code", 1
		} else {
			st.Active, st.Result, st.ExitStatus = "active", "success", 0
		}
	case "stop":
		st.Active = "inactive"
	}
	return nil
}

func (f *Fake) UnitState(_ context.Context, slug string) (UnitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st := f.states[slug]; st != nil {
		return *st, nil
	}
	return UnitState{Active: "inactive"}, nil
}

func (f *Fake) Journal(_ context.Context, slug string, lines int, _ bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(fmt.Sprintf("journal of %s (last %d lines)\n", UnitName(slug), lines))), nil
}

// Crash makes slug's service crash n times (systemd restarting it).
func (f *Fake) Crash(slug string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st := f.states[slug]; st != nil {
		st.NRestarts += n
		st.Result, st.ExitStatus = "exit-code", 2
	}
}

// FailStarts makes slug's starts fail (or work again).
func (f *Fake) FailStarts(slug string, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStart[slug] = fail
}

// Unit is slug's unit content ("" when it isn't installed).
func (f *Fake) Unit(slug string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Units[slug]
}

// StartCount is how often slug was (re)started.
func (f *Fake) StartCount(slug string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Starts[slug]
}

// BuildCount is how many builds ran.
func (f *Fake) BuildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Builds
}

// Active reports whether slug's unit is running.
func (f *Fake) Active(slug string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.states[slug]
	return st != nil && st.Active == "active"
}

var _ Runner = (*Fake)(nil)

// Exit makes slug's service stop for good with status (systemd not
// restarting it: Restart=no, say).
func (f *Fake) Exit(slug string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st := f.states[slug]; st != nil {
		st.Active, st.Result, st.ExitStatus = "failed", "exit-code", status
	}
}
