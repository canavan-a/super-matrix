package nixagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"main/tunnel"
)

// System is the real Runner: nix, systemctl and journalctl.
type System struct {
	NixBin, SystemctlBin, JournalctlBin, Bash string
	// UnitDir is where units go: /run/systemd/system (runtime units, so a
	// NixOS switch leaves them alone; the agent writes them again after a
	// reboot).
	UnitDir string
}

// NewSystem finds the tools on PATH.
func NewSystem() *System {
	find := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return name
	}
	return &System{NixBin: find("nix"), SystemctlBin: find("systemctl"), JournalctlBin: find("journalctl"), Bash: find("bash"),
		UnitDir: "/run/systemd/system"}
}

// nixArgs turns on flakes for each call, so the machine's nix.conf
// doesn't have to.
func (s *System) nixCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.NixBin, append([]string{"--extra-experimental-features", "nix-command flakes"}, args...)...)
	cmd.WaitDelay = 10e9
	return cmd
}

func output(cmd *exec.Cmd) (string, error) {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stripANSI(errb.String()))
		if i := strings.LastIndex(msg, "error: "); i >= 0 {
			msg = msg[i+len("error: "):]
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(clipMsg(msg))
	}
	return strings.TrimSpace(out.String()), nil
}

func clipMsg(s string) string {
	if len(s) > 1000 {
		return s[:1000] + "…"
	}
	return s
}

func (s *System) Host(ctx context.Context) tunnel.NixHost {
	h := tunnel.NixHost{}
	var problems []string
	if v, err := output(s.nixCmd(ctx, "--version")); err != nil {
		problems = append(problems, "nix isn't installed or doesn't run ("+err.Error()+")")
	} else {
		f := strings.Fields(v)
		h.Version = f[len(f)-1]
	}
	if h.Version != "" {
		if _, err := output(s.nixCmd(ctx, "flake", "--help")); err == nil {
			h.Flakes = true
		} else {
			problems = append(problems, "this nix has no flakes (needs 2.4 or newer)")
		}
		if sys, err := output(s.nixCmd(ctx, "eval", "--impure", "--raw", "--expr", "builtins.currentSystem")); err == nil {
			h.System = sys
		} else {
			problems = append(problems, "nix can't evaluate: "+err.Error())
		}
		_, err := output(s.nixCmd(ctx, "store", "info"))
		if err != nil {
			_, err = output(s.nixCmd(ctx, "store", "ping"))
		}
		if err == nil {
			h.Daemon = true
		} else {
			problems = append(problems, "the Nix store isn't reachable: "+err.Error())
		}
	}
	if fi, err := os.Stat("/run/systemd/system"); err == nil && fi.IsDir() {
		h.Systemd = true
	} else {
		problems = append(problems, "systemd isn't the init system")
	}
	h.OK = len(problems) == 0
	h.Error = strings.Join(problems, "; ")
	return h
}

// resolve turns a command's program into a path: absolute as is, with a
// slash relative to root, otherwise root/bin/<name>.
func resolve(prog, root string) string {
	switch {
	case filepath.IsAbs(prog):
		return prog
	case strings.Contains(prog, "/"):
		return filepath.Join(root, prog)
	}
	return filepath.Join(root, "bin", prog)
}

func (s *System) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
	spec := job.Spec
	ref := "path:" + job.Dir
	var attr string
	switch spec.Run {
	case tunnel.NixRunApp:
		attr = fmt.Sprintf("apps.%s.%s.program", job.System, spec.Attr)
	case tunnel.NixRunDevShell:
		attr = fmt.Sprintf("devShells.%s.%s", job.System, spec.Attr)
	default:
		attr = fmt.Sprintf("packages.%s.%s", job.System, spec.Attr)
	}
	installable := ref + "#" + attr
	var lockArgs []string
	if spec.LockPinned {
		lockArgs = []string{"--no-write-lock-file"}
	}
	pr := newProgress(job.Log, job.Progress)
	run := func(stdout io.Writer, args ...string) error {
		cmd := s.nixCmd(ctx, append(args, "--log-format", "internal-json")...)
		cmd.Dir = job.Dir
		cmd.Stdout, cmd.Stderr = stdout, pr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if msg := pr.err(); msg != "" {
				return errors.New(clipMsg(msg))
			}
			return err
		}
		return nil
	}

	var path string
	if spec.Run == tunnel.NixRunDevShell {
		profile := job.OutLink + ".profile"
		defer func() {
			links, _ := filepath.Glob(profile + "*")
			for _, l := range links {
				_ = os.Remove(l)
			}
		}()
		var env bytes.Buffer
		if err := run(&env, append([]string{"print-dev-env", installable, "--profile", profile}, lockArgs...)...); err != nil {
			return BuildResult{}, err
		}
		if err := os.WriteFile(job.EnvFile, env.Bytes(), 0o600); err != nil {
			return BuildResult{}, err
		}
		p, err := filepath.EvalSymlinks(profile)
		if err != nil {
			return BuildResult{}, fmt.Errorf("finding the dev shell's store path: %w", err)
		}
		path = p
		// Make OutLink the GC root (the profile links go).
		if _, err := output(s.nixCmd(ctx, "build", path, "--out-link", job.OutLink)); err != nil {
			return BuildResult{}, fmt.Errorf("keeping the dev shell: %w", err)
		}
	} else {
		var out bytes.Buffer
		if err := run(&out, append([]string{"build", installable, "--out-link", job.OutLink, "--print-out-paths"}, lockArgs...)...); err != nil {
			return BuildResult{}, err
		}
		path = strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
		if path == "" {
			return BuildResult{}, errors.New("nix built nothing")
		}
	}
	fmt.Fprintf(job.Log, "store path: %s\n", path)

	res := BuildResult{StorePath: path}
	cmd := spec.Command
	switch spec.Run {
	case tunnel.NixRunDevShell:
		res.Exec = append([]string{s.Bash, "-c", `. "$1"; shift; exec "$@"`, "smx-" + spec.Slug, job.EnvFile}, cmd...)
	case tunnel.NixRunApp:
		prog, err := output(s.nixCmd(ctx, "eval", "--raw", installable))
		if err != nil {
			return BuildResult{}, fmt.Errorf("finding the app's program: %w", err)
		}
		if len(cmd) == 0 {
			res.Exec = []string{prog}
		} else {
			res.Exec = append([]string{resolve(cmd[0], path)}, cmd[1:]...)
		}
	default:
		if len(cmd) == 0 {
			name, err := output(s.nixCmd(ctx, "eval", "--raw", installable+".meta.mainProgram"))
			if err != nil || name == "" {
				entries, _ := os.ReadDir(filepath.Join(path, "bin"))
				if len(entries) != 1 {
					return BuildResult{}, errors.New("the package doesn't name its main program (meta.mainProgram) and has more or less than one in bin/: set command")
				}
				name = entries[0].Name()
			}
			res.Exec = []string{filepath.Join(path, "bin", name)}
		} else {
			res.Exec = append([]string{resolve(cmd[0], path)}, cmd[1:]...)
		}
	}
	if _, err := os.Stat(res.Exec[0]); err != nil {
		return BuildResult{}, fmt.Errorf("%s isn't in the build", res.Exec[0])
	}
	return res, nil
}

func (s *System) unitPath(slug string) string { return filepath.Join(s.UnitDir, UnitName(slug)) }

func (s *System) InstallUnit(slug, content string) error {
	if err := os.MkdirAll(s.UnitDir, 0o755); err != nil {
		return err
	}
	tmp := s.unitPath(slug) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.unitPath(slug)); err != nil {
		return err
	}
	_, err := output(exec.Command(s.SystemctlBin, "daemon-reload"))
	return err
}

func (s *System) RemoveUnit(slug string) error {
	if err := os.Remove(s.unitPath(slug)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := output(exec.Command(s.SystemctlBin, "daemon-reload"))
	_, _ = output(exec.Command(s.SystemctlBin, "reset-failed", UnitName(slug)))
	return err
}

func (s *System) Systemctl(ctx context.Context, verb, slug string) error {
	_, err := output(exec.CommandContext(ctx, s.SystemctlBin, verb, UnitName(slug)))
	return err
}

func (s *System) UnitState(ctx context.Context, slug string) (UnitState, error) {
	out, err := output(exec.CommandContext(ctx, s.SystemctlBin, "show", UnitName(slug),
		"-p", "LoadState,ActiveState,SubState,NRestarts,ExecMainStatus,Result"))
	if err != nil {
		return UnitState{}, err
	}
	st := UnitState{}
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "LoadState":
			st.Loaded = v == "loaded"
		case "ActiveState":
			st.Active = v
		case "SubState":
			st.Sub = v
		case "NRestarts":
			st.NRestarts, _ = strconv.Atoi(v)
		case "ExecMainStatus":
			st.ExitStatus, _ = strconv.Atoi(v)
		case "Result":
			st.Result = v
		}
	}
	return st, nil
}

func (s *System) Journal(ctx context.Context, slug string, lines int, follow bool) (io.ReadCloser, error) {
	args := []string{"-u", UnitName(slug), "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso"}
	if follow {
		args = append(args, "-f")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, s.JournalctlBin, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return &procReader{ReadCloser: out, cancel: cancel, cmd: cmd}, nil
}

type procReader struct {
	io.ReadCloser
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

func (p *procReader) Close() error {
	p.cancel()
	err := p.ReadCloser.Close()
	_ = p.cmd.Wait()
	return err
}

var _ Runner = (*System)(nil)
