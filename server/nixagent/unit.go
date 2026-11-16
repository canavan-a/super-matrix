package nixagent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"main/tunnel"
)

// UnitName is the systemd unit a service runs as.
func UnitName(slug string) string { return "smx-" + slug + ".service" }

// execQuote quotes one word of an ExecStart= line: double quotes, with \
// and " escaped, and % and $ doubled so systemd takes them literally
// (ExecStart= expands both).
func execQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$", "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// envQuote quotes an Environment= assignment. systemd expands % there but
// not $, so only % is doubled: a value like pa$word arrives as it is.
func envQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// pathValue is a path for a setting that takes it as is (WorkingDirectory=
// doesn't unquote): only % is escaped.
func pathValue(p string) string { return strings.ReplaceAll(p, "%", "%%") }

// unitFile is the systemd unit for spec running exec (from a build) with
// working directory dir ("" for the state directory). envFile is the
// relay's own env file for the service.
func unitFile(spec tunnel.NixService, exec []string, dir, envFile string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# Written by the Super Matrix Nix agent for the Nix service %q.", spec.Slug)
	w("# It's replaced on every change: edit the service in Super Matrix instead.")
	w("[Unit]")
	w("Description=Super Matrix Nix service %s", spec.Slug)
	w("After=network-online.target")
	w("Wants=network-online.target")
	// The agent counts crashes itself; systemd shouldn't give up.
	w("StartLimitIntervalSec=0")
	w("")
	w("[Service]")
	words := make([]string, len(exec))
	for i, e := range exec {
		words[i] = execQuote(e)
	}
	w("ExecStart=%s", strings.Join(words, " "))
	w("StateDirectory=smx/%s", spec.Slug)
	if dir == "" {
		w("WorkingDirectory=%%S/smx/%s", spec.Slug)
	} else {
		w("WorkingDirectory=%s", pathValue(dir))
	}
	w("Environment=%s", envQuote("SMX_SERVICE="+spec.Slug))
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w("Environment=%s", envQuote(k+"="+spec.Env[k]))
	}
	// The relay's own file wins over env, and may be missing.
	w("EnvironmentFile=-%s", envFile)
	switch spec.Restart {
	case "no", "on-failure":
		w("Restart=%s", spec.Restart)
	default:
		w("Restart=always")
	}
	w("RestartSec=2")
	return b.String()
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// filesHash fingerprints a service's files (not the lock): a devshell
// runs from a directory named by it.
func filesHash(spec tunnel.NixService) string {
	keys := make([]string, 0, len(spec.Files))
	for k := range spec.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(spec.Files[k]), spec.Files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseEnvFile reads KEY=VALUE lines the way systemd's EnvironmentFile
// does, enough to tell which names are set.
func parseEnvFile(data string) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(data, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		l = strings.TrimPrefix(l, "export ")
		k, _, ok := strings.Cut(l, "=")
		if ok {
			out[strings.TrimSpace(k)] = true
		}
	}
	return out
}
