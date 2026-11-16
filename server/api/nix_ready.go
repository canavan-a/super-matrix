package api

import (
	"encoding/json"
	"fmt"

	"main/database"
	"main/tunnel"
)

// Whether a relay can run Nix services comes down to four checks, made in
// order from what its last hello said (so they hold while it's offline):
//
//  1. update: its build lists the "nix" feature. Older relays can't carry
//     Nix services at all and have to be updated first.
//  2. agent: it lists "nix-services": the Nix agent on its machine (a
//     separate root service, which the machine's owner turns on) answered.
//  3. host: the agent's host check passed: Nix with flakes, a reachable
//     store, systemd.
//  4. allow: Nix services are turned on for the relay in the app.
//
// Only a relay that passes all four can have services applied to it.

// Nix readiness steps: the first check a relay fails.
const (
	nixStepUpdate = "update"
	nixStepAgent  = "agent"
	nixStepHost   = "host"
	nixStepAllow  = "allow"
)

type nixReadyDTO struct {
	// Supported: the relay's build can carry Nix services.
	Supported bool `json:"supported"`
	// Agent: the Nix agent on its machine answered (as of its last hello).
	Agent bool `json:"agent"`
	// Host is the agent's last host check (nil without one).
	Host *tunnel.NixHost `json:"host"`
	// Allowed: Nix services are turned on for it in the app.
	Allowed bool `json:"allowed"`
	// Ready: all of the above; services can be applied to it.
	Ready bool `json:"ready"`
	// Step is the first check it fails ("" when Ready), Reason what to do.
	Step   string `json:"step"`
	Reason string `json:"reason"`
}

func nixHostOf(r database.Relay) *tunnel.NixHost {
	if r.NixHost == "" {
		return nil
	}
	var h tunnel.NixHost
	if json.Unmarshal([]byte(r.NixHost), &h) != nil {
		return nil
	}
	return &h
}

// nixReadiness works out whether r can run Nix services, and if not why.
func nixReadiness(r database.Relay) nixReadyDTO {
	out := nixReadyDTO{
		Supported: r.HasFeature(tunnel.FeatureNix),
		Agent:     r.HasFeature(tunnel.FeatureNixServices),
		Host:      nixHostOf(r),
		Allowed:   r.AllowNixServices,
	}
	ver := r.Version
	if ver == "" {
		ver = "unknown"
	}
	switch {
	case r.LastSeenAt == nil:
		out.Step, out.Reason = nixStepUpdate, r.Name+" hasn't connected yet: start it, so it can say what it supports"
	case !out.Supported:
		out.Step, out.Reason = nixStepUpdate, fmt.Sprintf("%s runs a relay (version %s) too old for Nix services: update supermatrix-relay on it", r.Name, ver)
	case !out.Agent:
		out.Step, out.Reason = nixStepAgent, "there's no Nix agent running on "+r.Name+"'s machine: turn on services.supermatrix-relay.nixServices.enable (NixOS) or run install.sh relay --nix-services"
	case out.Host == nil || !out.Host.OK:
		msg := "its host check hasn't run yet"
		if out.Host != nil && out.Host.Error != "" {
			msg = out.Host.Error
		}
		out.Step, out.Reason = nixStepHost, r.Name+"'s machine can't run Nix services: "+msg
	case !out.Allowed:
		out.Step, out.Reason = nixStepAllow, "Nix services aren't turned on for "+r.Name+": turn them on in Relays"
	default:
		out.Ready = true
	}
	return out
}

// nixHostJSON is a hello's host check as stored ("" without one).
func nixHostJSON(hp *tunnel.NixHost) string {
	if hp == nil {
		return ""
	}
	h := *hp // the hub keeps the original
	h.Version, h.System, h.Error = clip(h.Version, 40), clip(h.System, 40), clip(h.Error, 500)
	b, _ := json.Marshal(h)
	return string(b)
}
