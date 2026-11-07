// Command supermatrix-admin is the operator's view of a Super Matrix
// database, for when the app isn't at hand.
//
//	supermatrix-admin stats
//	supermatrix-admin list
//	supermatrix-admin revoke-sessions
//	supermatrix-admin rate-limit HOST
//	supermatrix-admin rate-limit HOST on REQUESTS PERIOD_S [BURST]
//	supermatrix-admin rate-limit HOST off
//	supermatrix-admin path-rules HOST
//	supermatrix-admin path-rules HOST add PREFIX [CIDR...]
//	supermatrix-admin path-rules HOST set PREFIX [CIDR...]
//	supermatrix-admin path-rules HOST remove PREFIX
//
// rate-limit and path-rules write straight to the database, like every
// other command here: the running server only rereads it at startup, so
// restart supermatrix-server for a change to take effect.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"gorm.io/gorm"

	"main/database"
	"main/registry"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	dataDir := os.Getenv("SUPERMATRIX_DATA_DIR")
	if dataDir == "" {
		dataDir = ".data"
	}
	dbPath := os.Getenv("SUPERMATRIX_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "supermatrix.db")
	}
	db, err := database.Connect(dbPath)
	if err != nil {
		fail(err)
	}

	switch os.Args[1] {
	case "stats":
		count := func(table string) int64 {
			var n int64
			db.Table(table).Count(&n)
			return n
		}
		fmt.Printf("domains         %d\n", count("domains"))
		fmt.Printf("relays          %d\ndeployments     %d\ntokens          %d\nadmin sessions  %d\n",
			count("relays"), count("deployments"), count("deployment_tokens"), count("admin_sessions"))
	case "list":
		var relays []database.Relay
		db.Order("name").Find(&relays)
		names := map[string]string{}
		for _, r := range relays {
			names[r.ID] = r.Name
		}
		var doms []database.Domain
		db.Order("name").Find(&doms)
		var ds []database.Deployment
		db.Order("host").Find(&ds)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "DOMAIN")
		for _, d := range doms {
			fmt.Fprintln(w, d.Label())
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "RELAY\tLAST SEEN\tFROM\tTRANSPORT\tVERSION")
		for _, r := range relays {
			seen := "never"
			if r.LastSeenAt != nil {
				seen = r.LastSeenAt.Local().Format("2006-01-02 15:04")
			}
			tr := r.Transport
			if tr == "websocket" && r.Fallback != "" {
				tr += " (not QUIC: " + r.Fallback + ")"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, seen, r.LastIP, tr, r.Version)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "HOST\tRELAY\tTARGET\tAUTH\tCERT\tENABLED")
		for _, d := range ds {
			host := "(no domain)"
			if d.Host != nil {
				host = *d.Host
			}
			fmt.Fprintf(w, "%s\t%s\t%s://%s:%d\t%s\t%s\t%v\n", host, names[d.RelayID], d.Protocol, d.TargetHost, d.TargetPort, d.AuthMode, d.CertStatus, d.Enabled)
		}
		w.Flush()
	case "revoke-sessions":
		res := db.Where("1 = 1").Delete(&database.AdminSession{})
		fmt.Printf("signed out %d app session(s)\n", res.RowsAffected)
	case "rate-limit":
		rateLimitCmd(db, os.Args[2:])
	case "path-rules":
		pathRulesCmd(db, os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: supermatrix-admin stats | list | revoke-sessions | rate-limit HOST ... | path-rules HOST ...")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// deploymentByHost finds the deployment at host, or exits with an error.
func deploymentByHost(db *gorm.DB, host string) *database.Deployment {
	d, err := database.First[database.Deployment](db.Where("host = ?", host))
	if err != nil {
		fail(err)
	}
	if d == nil {
		fail(fmt.Errorf("no deployment at %s", host))
	}
	return d
}

// restartNote is printed after a write that the edge only picks up at
// startup, since this tool (unlike the app) talks to the database directly.
const restartNote = "saved; restart supermatrix-server to pick it up (it only rereads the database at startup)"

func rateLimitUsage() {
	fmt.Fprintln(os.Stderr, "usage: supermatrix-admin rate-limit HOST [on REQUESTS PERIOD_S [BURST] | off]")
	os.Exit(2)
}

func rateLimitCmd(db *gorm.DB, args []string) {
	if len(args) < 1 {
		rateLimitUsage()
	}
	d := deploymentByHost(db, args[0])
	if len(args) == 1 {
		if !d.RateLimitEnabled {
			fmt.Println("off")
			return
		}
		period := "second"
		if d.RateLimitPeriodS == 60 {
			period = "minute"
		}
		fmt.Printf("on: %d requests per %s, burst %d\n", d.RateLimitRequests, period, d.RateLimitBurst)
		return
	}
	switch args[1] {
	case "off":
		if err := db.Model(d).Update("rate_limit_enabled", false).Error; err != nil {
			fail(err)
		}
	case "on":
		if len(args) < 4 {
			rateLimitUsage()
		}
		requests, err := strconv.Atoi(args[2])
		if err != nil || requests < 1 {
			fail(errors.New("requests must be a positive number"))
		}
		periodS, err := strconv.Atoi(args[3])
		if err != nil || (periodS != 1 && periodS != 60) {
			fail(errors.New("period must be 1 (per second) or 60 (per minute)"))
		}
		burst := 0
		if len(args) > 4 {
			if burst, err = strconv.Atoi(args[4]); err != nil || burst < 0 {
				fail(errors.New("burst can't be negative"))
			}
		}
		updates := map[string]any{
			"rate_limit_enabled": true, "rate_limit_requests": requests,
			"rate_limit_period_s": periodS, "rate_limit_burst": burst,
		}
		if err := db.Model(d).Updates(updates).Error; err != nil {
			fail(err)
		}
	default:
		rateLimitUsage()
	}
	fmt.Println(restartNote)
}

func pathRulesUsage() {
	fmt.Fprintln(os.Stderr, "usage: supermatrix-admin path-rules HOST [add PREFIX [CIDR...] | set PREFIX [CIDR...] | remove PREFIX]")
	os.Exit(2)
}

func pathRulesCmd(db *gorm.DB, args []string) {
	if len(args) < 1 {
		pathRulesUsage()
	}
	d := deploymentByHost(db, args[0])
	if len(args) == 1 {
		var rules []database.PathRule
		db.Where("deployment_id = ?", d.ID).Order("path_prefix").Find(&rules)
		if len(rules) == 0 {
			fmt.Println("no path rules")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PREFIX\tIP ALLOW LIST")
		for _, r := range rules {
			allow := strings.ReplaceAll(strings.TrimSpace(r.IPAllowlist), "\n", ", ")
			if allow == "" {
				allow = "anyone"
			}
			fmt.Fprintf(w, "%s\t%s\n", r.PathPrefix, allow)
		}
		w.Flush()
		return
	}
	switch args[1] {
	case "add", "set":
		if len(args) < 3 {
			pathRulesUsage()
		}
		prefix, err := normalizePathPrefix(args[2])
		if err != nil {
			fail(err)
		}
		ips := args[3:]
		if _, err := registry.ParseAllowlist(strings.Join(ips, "\n")); err != nil {
			fail(fmt.Errorf("IP allow list: %w", err))
		}
		allow := strings.Join(ips, "\n")
		now := time.Now()
		existing, err := database.First[database.PathRule](db.Where("deployment_id = ? AND path_prefix = ?", d.ID, prefix))
		if err != nil {
			fail(err)
		}
		if existing == nil {
			r := database.PathRule{ID: database.NewID(), DeploymentID: d.ID, PathPrefix: prefix, IPAllowlist: allow, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&r).Error; err != nil {
				fail(err)
			}
		} else if err := db.Model(existing).Updates(map[string]any{"ip_allowlist": allow, "updated_at": now}).Error; err != nil {
			fail(err)
		}
	case "remove":
		if len(args) < 3 {
			pathRulesUsage()
		}
		res := db.Where("deployment_id = ? AND path_prefix = ?", d.ID, args[2]).Delete(&database.PathRule{})
		if res.RowsAffected == 0 {
			fail(fmt.Errorf("no rule for %s on %s", args[2], args[0]))
		}
	default:
		pathRulesUsage()
	}
	fmt.Println(restartNote)
}

// normalizePathPrefix mirrors the app's own check (server/api/path_rules.go):
// must start with "/", can't end with "/" (except "/" itself), no
// whitespace, a sane length cap.
func normalizePathPrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s[0] != '/':
		return "", errors.New(`path prefix must start with "/"`)
	case len(s) > 200:
		return "", errors.New("path prefix: at most 200 characters")
	case s != "/" && strings.HasSuffix(s, "/"):
		return "", errors.New(`path prefix can't end with "/" (except "/" itself)`)
	case strings.ContainsAny(s, " \t\r\n"):
		return "", errors.New("path prefix can't contain whitespace")
	}
	return s, nil
}
