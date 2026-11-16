package nixagent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"main/tunnel"
)

// TestSystemBuild builds a real flake three ways with the machine's Nix.
// It needs nix (and its nixpkgs, from the registry): set
// SUPERMATRIX_NIX_TEST=1 to run it.
func TestSystemBuild(t *testing.T) {
	if os.Getenv("SUPERMATRIX_NIX_TEST") != "1" {
		t.Skip("set SUPERMATRIX_NIX_TEST=1 to build with the real nix")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("no nix")
	}
	s := NewSystem()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	h := s.Host(ctx)
	if h.Version == "" || h.System == "" || !h.Flakes {
		t.Fatalf("host: %+v", h)
	}
	flake := `{
  inputs.nixpkgs.url = "nixpkgs";
  outputs = { nixpkgs, ... }: let pkgs = nixpkgs.legacyPackages.` + h.System + `; in {
    packages.` + h.System + `.default = pkgs.writeShellScriptBin "smx-test" "echo package";
    apps.` + h.System + `.default = { type = "app"; program = "${pkgs.writeShellScript "smx-app" "echo app"}"; };
    devShells.` + h.System + `.default = pkgs.mkShell { SMX_TEST = "devshell"; };
  };
}`
	for _, run := range []string{tunnel.NixRunPackage, tunnel.NixRunApp, tunnel.NixRunDevShell} {
		t.Run(run, func(t *testing.T) {
			dir := t.TempDir()
			spec := tunnel.NixService{ID: "t", Slug: "smx-test", Run: run, Attr: "default", Files: map[string]string{"flake.nix": flake}}
			if run == tunnel.NixRunDevShell {
				spec.Command = []string{"sh", "-c", "echo $SMX_TEST"}
			}
			if err := writeFiles(filepath.Join(dir, "src"), spec); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			var steps []string
			job := BuildJob{Spec: spec, Dir: filepath.Join(dir, "src"), System: h.System, OutLink: filepath.Join(dir, "root"),
				EnvFile: filepath.Join(dir, "env.sh"), Log: &log,
				Progress: func(p tunnel.NixProgress) { steps = append(steps, p.Step) }}
			res, err := s.Build(ctx, job)
			if err != nil {
				t.Fatalf("%v\n%s", err, log.String())
			}
			if !strings.HasPrefix(res.StorePath, "/nix/store/") {
				t.Fatalf("store path %q", res.StorePath)
			}
			if target, err := filepath.EvalSymlinks(job.OutLink); err != nil || target != res.StorePath {
				t.Fatalf("gc root %q → %q (%v)", job.OutLink, target, err)
			}
			if _, err := os.Stat(filepath.Join(job.Dir, "flake.lock")); err != nil {
				t.Fatalf("no flake.lock: %v", err)
			}
			out, err := exec.Command(res.Exec[0], res.Exec[1:]...).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != run {
				t.Fatalf("running %v: %q %v", res.Exec, out, err)
			}
			if len(steps) == 0 {
				t.Fatalf("no progress")
			}
		})
	}

	t.Run("error", func(t *testing.T) {
		dir := t.TempDir()
		spec := tunnel.NixService{ID: "t", Slug: "x", Run: tunnel.NixRunPackage, Attr: "nope", Files: map[string]string{"flake.nix": flake}}
		_ = writeFiles(dir, spec)
		_, err := s.Build(ctx, BuildJob{Spec: spec, Dir: dir, System: h.System, OutLink: filepath.Join(dir, "root"), Log: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), "does not provide attribute") || strings.Contains(err.Error(), "\x1b[") {
			t.Fatalf("error: %v", err)
		}
	})
}
