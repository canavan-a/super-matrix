package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"main/nixagent"
)

// The Nix agent: `supermatrix-relay nix-agent`, a separate root service
// on machines whose owner lets the relay run Nix services. It builds the
// services' flakes and runs them as systemd units; the relay (which stays
// unprivileged) only passes it what the server says.
//
//	SUPERMATRIX_NIX_SOCKET=/run/supermatrix-nix/agent.sock   where it listens (the relay looks there too)
//	SUPERMATRIX_NIX_GROUP=supermatrix-nix                     the group that may use the socket (the relay's)
//	SUPERMATRIX_NIX_DIR=/var/lib/supermatrix-nix              its own state
//	SUPERMATRIX_NIX_ENV_DIR=/etc/supermatrix/nix-services      each service's <slug>.env, written by you
//
// Anything that can use the socket can run programs as root on this
// machine, and so can whoever controls the Super Matrix server.

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func nixSocket() string { return env("SUPERMATRIX_NIX_SOCKET", nixagent.DefaultSocket) }

func runNixAgent() {
	if os.Geteuid() != 0 {
		log.Fatal("the Nix agent runs services as systemd units: run it as root (its NixOS module and install.sh relay --nix-services do)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m := nixagent.NewManager(nixagent.Config{
		Dir:       env("SUPERMATRIX_NIX_DIR", "/var/lib/supermatrix-nix"),
		EnvDir:    env("SUPERMATRIX_NIX_ENV_DIR", "/etc/supermatrix/nix-services"),
		StateRoot: "/var/lib/smx",
		Runner:    nixagent.NewSystem(),
	})
	if err := os.MkdirAll(env("SUPERMATRIX_NIX_ENV_DIR", "/etc/supermatrix/nix-services"), 0o700); err != nil {
		log.Printf("nix agent: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		log.Fatalf("nix agent: %v", err)
	}
	ln, err := nixagent.Listen(nixSocket(), env("SUPERMATRIX_NIX_GROUP", "supermatrix-nix"))
	if err != nil {
		log.Fatalf("nix agent: listening on %s: %v", nixSocket(), err)
	}
	log.Printf("nix agent: listening on %s", nixSocket())
	if err := m.Serve(ctx, ln); err != nil {
		log.Fatalf("nix agent: %v", err)
	}
}
