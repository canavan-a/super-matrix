// Command supermatrix-admin is the operator's view of a Super Matrix
// database, for when the app isn't at hand.
//
//	supermatrix-admin stats
//	supermatrix-admin list
//	supermatrix-admin revoke-sessions
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"main/database"
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
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: supermatrix-admin stats | list | revoke-sessions")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
