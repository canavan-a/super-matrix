package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"time"
)

// Nix services: a relay whose machine runs the Nix agent (supermatrix-relay
// nix-agent, a separate root service) builds and runs the Nix services the
// server applies to it. The relay itself stays unprivileged; it only passes
// messages between the server and the agent's Unix socket.
//
//	relay → server  hello.features += "nix-services"; hello.nix = {ok, version, …}
//	server → relay  {"type":"nixServices","nixServices":[…]}      the full set, like targets
//	relay → server  {"type":"nixStatus","nixStatus":{"<id>":{…}}}  phases, as they change
//	relay → server  {"type":"nixLock","nixLock":{…}}               a flake.lock the relay made
//	server → relay  {"type":"nixAction","nixAction":{…}}           restart | rebuild | rollback
//	relay → server  {"type":"nixKnown","nixKnown":["<id>",…]}      what the agent still manages
//
// Logs go over a data stream the server opens with {"nixLogs":"<id>"}.

// Two features gate Nix services. FeatureNix is listed by every relay
// build that can carry them: a relay without it is too old, and needs
// updating first. FeatureNixServices is listed on top while the Nix agent
// on the relay's machine answers; its host check is in Hello.Nix.
const (
	FeatureNix         = "nix"
	FeatureNixServices = "nix-services"
)

// Ways a Nix service runs.
const (
	NixRunPackage  = "package"
	NixRunApp      = "app"
	NixRunDevShell = "devshell"
)

// Desired states.
const (
	NixRunning = "running"
	NixStopped = "stopped"
)

// Phases an applied Nix service goes through on the relay.
const (
	PhasePending           = "pending"
	PhasePreparing         = "preparing"
	PhaseBuilding          = "building"
	PhaseStarting          = "starting"
	PhaseRunning           = "running"
	PhaseStopping          = "stopping"
	PhaseStopped           = "stopped"
	PhaseMissingEnv        = "missing-env"
	PhaseBuildFailed       = "build-failed"
	PhaseStartFailed       = "start-failed"
	PhaseCrashed           = "crashed"
	PhaseCrashLoop         = "crash-loop"
	PhaseTearingDown       = "tearing-down"
	PhaseRemoved           = "removed"
	PhaseRemovedWithErrors = "removed-with-errors"
)

// FailedPhases are the phases the app flags as failing.
var FailedPhases = []string{PhaseMissingEnv, PhaseBuildFailed, PhaseStartFailed, PhaseCrashed, PhaseCrashLoop, PhaseRemovedWithErrors}

// FailedPhase reports whether phase is one of FailedPhases.
func FailedPhase(phase string) bool { return slices.Contains(FailedPhases, phase) }

// NixHost is the agent's check that this machine can run Nix services.
type NixHost struct {
	// OK: Nix with flakes, a reachable store and systemd as init.
	OK bool `json:"ok"`
	// Version is `nix --version`'s number, e.g. "2.24.9".
	Version string `json:"version"`
	Flakes  bool   `json:"flakes"`
	Daemon  bool   `json:"daemon"`
	Systemd bool   `json:"systemd"`
	// System is the Nix system, e.g. x86_64-linux.
	System string `json:"system"`
	// Error says what's missing when OK is false.
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// NixService is one applied Nix service as the relay is told it: the
// service's definition with the relay's overrides merged in. ID is the
// applied service's ID, not the service's.
type NixService struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	// Run is package | app | devshell; Attr picks the output
	// (packages.<system>.<attr>, apps.<system>.<attr>, devShells.<system>.<attr>).
	Run     string            `json:"run"`
	Attr    string            `json:"attr"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// RelayEnv names variables that must be set in the relay's own
	// /etc/supermatrix/nix-services/<slug>.env.
	RelayEnv    []string `json:"relayEnv,omitempty"`
	Restart     string   `json:"restart"`
	RemoveState bool     `json:"removeState"`
	// Files are the flake's files by path: flake.nix and any others.
	Files map[string]string `json:"files"`
	// Lock is flake.lock ("" for none yet). LockPinned: it was given, so the
	// relay builds with it as is and never sends one back.
	Lock       string `json:"lock,omitempty"`
	LockPinned bool   `json:"lockPinned,omitempty"`
	Desired    string `json:"desired"`
}

// BuildHash fingerprints what a build depends on: files, lock, and how
// the output is picked. A change to any of it means a rebuild.
func (s NixService) BuildHash() string {
	return hashJSON(struct {
		Run, Attr, Lock string
		Files           [][2]string
	}{s.Run, s.Attr, s.Lock, sortedPairs(s.Files)})
}

// UnitHash fingerprints what the systemd unit depends on beyond the
// build: a change means a restart, not a rebuild.
func (s NixService) UnitHash() string {
	return hashJSON(struct {
		Slug, Restart string
		Command       []string
		Env           [][2]string
		RelayEnv      []string
	}{s.Slug, s.Restart, s.Command, sortedPairs(s.Env), s.RelayEnv})
}

func sortedPairs(m map[string]string) [][2]string {
	out := make([][2]string, 0, len(m))
	for k, v := range m {
		out = append(out, [2]string{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// HashLock is how a flake.lock is named in NixLock.Base ("" for none).
func HashLock(lock string) string {
	if lock == "" {
		return ""
	}
	h := sha256.Sum256([]byte(lock))
	return hex.EncodeToString(h[:])
}

// NixProgress is how far a build is, from Nix's own progress reports.
type NixProgress struct {
	// Step: evaluating | fetching | downloading | building | copying.
	Step       string `json:"step"`
	Built      int    `json:"built"`
	ToBuild    int    `json:"toBuild"`
	Downloaded int    `json:"downloaded"`
	ToDownload int    `json:"toDownload"`
	// Current is what's being built or fetched, e.g. nodejs-20.11.1.
	Current  string `json:"current,omitempty"`
	ElapsedS int    `json:"elapsedS"`
}

// NixStatus is an applied service's state on the relay.
type NixStatus struct {
	Phase string    `json:"phase"`
	Since time.Time `json:"since"`
	// Serving is the store path running now ("" if none); Previous the one
	// before it, for rollback. While a new build runs the old one keeps
	// serving.
	Serving  string `json:"serving,omitempty"`
	Previous string `json:"previous,omitempty"`
	// BuildHash is the NixService.BuildHash that Serving was built from.
	BuildHash string `json:"buildHash,omitempty"`
	Error     string `json:"error,omitempty"`
	// Log is the tail of the build log or the journal for a failure.
	Log        string       `json:"log,omitempty"`
	MissingEnv []string     `json:"missingEnv,omitempty"`
	ExitCode   *int         `json:"exitCode,omitempty"`
	Restarts   int          `json:"restarts,omitempty"`
	Progress   *NixProgress `json:"progress,omitempty"`
	// Event, when set, is what just happened (build-ok, started, crashed …)
	// for the server's event history; phases alone don't say that a build
	// succeeded while the old version kept running.
	Event string `json:"event,omitempty"`
	// Teardown lists the steps that failed removing the service.
	Teardown []string `json:"teardown,omitempty"`
}

// NixLock is a flake.lock the relay made or updated building a service
// whose lock isn't pinned. Base is HashLock of the lock it was given: the
// server keeps the first relay's lock and ignores the rest.
type NixLock struct {
	ID   string `json:"id"`
	Base string `json:"base"`
	Lock string `json:"lock"`
}

// Nix actions.
const (
	NixRestart  = "restart"
	NixRebuild  = "rebuild"
	NixRollback = "rollback"
)

// NixAction asks the relay to do something to one applied service.
type NixAction struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// NixLogs is what a nixLogs stream asks for: the journal (Lines of it,
// following when Follow) or, with Build, the last build's full log.
type NixLogs struct {
	Lines  int  `json:"lines,omitempty"`
	Follow bool `json:"follow,omitempty"`
	Build  bool `json:"build,omitempty"`
}

// NixAgent is the relay's line to the Nix agent on its machine (package
// nixagent has the real one, over its Unix socket).
type NixAgent interface {
	// Host is the agent's host check; nil while the agent isn't
	// reachable (the relay then doesn't list nix-services).
	Host() *NixHost
	// Set gives the agent the full set of services to run. It answers
	// with the status of everything it manages, and what that is (Known).
	Set(set []NixService)
	Action(act NixAction)
	// Logs streams a service's logs as text until it ends or ctx does.
	Logs(ctx context.Context, id string, req NixLogs) (io.ReadCloser, error)
	// Notify is signalled when Take has something.
	Notify() <-chan struct{}
	// Take returns what the agent reported since the last Take; Requeue
	// puts back what couldn't be sent (the server link dropped), ahead of
	// anything newer.
	Take() NixReport
	Requeue(r NixReport)
}

// NixReport is what a NixAgent has to say, batched.
type NixReport struct {
	// Status is each report in the order the agent made them.
	Status []map[string]NixStatus
	Locks  []NixLock
	// Known is set (HasKnown) when the agent listed what it manages.
	Known    []string
	HasKnown bool
	// HostChanged: the agent came or went, or its host check changed;
	// the relay says hello again.
	HostChanged bool
}
