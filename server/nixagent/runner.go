// Package nixagent is the Nix agent: the root service (supermatrix-relay
// nix-agent) that builds and runs the Nix services a relay is given. The
// relay, which stays unprivileged, talks to it over a Unix socket (Client
// is the relay's side). The agent keeps its own state, so services keep
// running, and come back after a reboot, while the relay or the server is
// away.
//
// Each applied service becomes a systemd unit, smx-<slug>.service, written
// to /run/systemd/system from a store path the agent built (and holds a GC
// root to). The previous build is kept for rollback.
package nixagent

import (
	"context"
	"encoding/json"
	"io"

	"main/tunnel"
)

// Runner is what the agent needs from the machine: Nix, systemd and the
// journal. System is the real one; tests use a fake.
type Runner interface {
	// Host checks Nix and systemd.
	Host(ctx context.Context) tunnel.NixHost
	// Build builds job's flake and makes job.OutLink a GC root to the
	// result.
	Build(ctx context.Context, job BuildJob) (BuildResult, error)
	// InstallUnit writes smx-<slug>.service and reloads systemd.
	InstallUnit(slug, content string) error
	// RemoveUnit deletes it, reloads systemd and forgets its failures.
	RemoveUnit(slug string) error
	// Systemctl runs start | stop | restart on smx-<slug>.service.
	Systemctl(ctx context.Context, verb, slug string) error
	UnitState(ctx context.Context, slug string) (UnitState, error)
	// Journal is smx-<slug>'s last lines of journal, then (follow) new
	// ones until ctx ends.
	Journal(ctx context.Context, slug string, lines int, follow bool) (io.ReadCloser, error)
}

// BuildJob is one build of a service's flake, in Dir (its files already
// written there).
type BuildJob struct {
	Spec tunnel.NixService
	Dir  string
	// System is the Nix system to pick outputs for (x86_64-linux …).
	System string
	// OutLink becomes a GC root to what was built.
	OutLink string
	// EnvFile (devshell) is where to write the shell's environment.
	EnvFile string
	// Log gets the build's log as text; Progress how far it is.
	Log      io.Writer
	Progress func(tunnel.NixProgress)
}

// BuildResult is what a build made: the store path and the command line
// that runs it.
type BuildResult struct {
	StorePath string
	Exec      []string
}

// UnitState is a unit's state as systemd has it.
type UnitState struct {
	// Active is active | activating | deactivating | inactive | failed.
	Active string
	Sub    string
	// NRestarts counts automatic restarts since the unit was started.
	NRestarts int
	// ExitStatus is the main process's last exit status.
	ExitStatus int
	// Result is success, exit-code, signal …
	Result string
	// Loaded is false when systemd doesn't know the unit.
	Loaded bool
}

// msg is one line on the agent's socket.
//
//	relay → agent  {"type":"hello"}                     first, on the control connection
//	relay → agent  {"type":"set","services":[…]}
//	relay → agent  {"type":"action","action":{…}}
//	relay → agent  {"type":"logs","id":…,"logs":{…}}   (on a connection of its own)
//	agent → relay  {"type":"host","host":{…}}
//	agent → relay  {"type":"status","status":{"<id>":{…}}}
//	agent → relay  {"type":"lock","lock":{…}}
//	agent → relay  {"type":"known","known":[…]}
//	agent → relay  {"type":"logs"} or {"type":"logs","error":…}, then the text
type msg struct {
	Type     string                      `json:"type"`
	Services []tunnel.NixService         `json:"services,omitempty"`
	Action   *tunnel.NixAction           `json:"action,omitempty"`
	ID       string                      `json:"id,omitempty"`
	Logs     *tunnel.NixLogs             `json:"logs,omitempty"`
	Host     *tunnel.NixHost             `json:"host,omitempty"`
	Status   map[string]tunnel.NixStatus `json:"status,omitempty"`
	Lock     *tunnel.NixLock             `json:"lock,omitempty"`
	Known    []string                    `json:"known,omitempty"`
	Error    string                      `json:"error,omitempty"`
}

func writeMsg(w io.Writer, m msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
