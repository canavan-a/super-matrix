package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// schema plan|apply|export talks to a running server over HTTP, unlike the
// other commands here: the server checks the document with the same
// validators the app's edits go through, makes the change in one
// transaction, and puts it into effect at once, without a restart.
//
// Exit codes: 0 success (plan: no changes), 2 plan has changes, 1 error.

const schemaUsage = `usage: supermatrix-admin schema plan|apply|export|nix-status [flags]

  plan        show what applying the document would change (exit 2 if anything)
  apply       apply the document, printing what it changed
  export      print the server's state as a document (or the last applied one)
  nix-status  what each Nix service is doing on each relay

flags:
  -f FILE          the document, YAML or JSON (default: standard input)
  -server URL      the server, e.g. https://admin.example.com ($SUPERMATRIX_SERVER)
  -token TOKEN     an API token (sma_…) with the schema scope ($SUPERMATRIX_TOKEN)
  -allow-outside   let source: and {file: …} read outside the document's directory
  -wait            apply: then follow the Nix services until they run (exit 1 if one fails)
  -wait-timeout D  how long -wait waits (default 15m)
  -source SRC      export: current (default) or applied
  -format FMT      export: yaml (default) or json
  -o FILE          export: write to FILE instead of standard output
  -split DIR       export: write each Nix service's files to DIR/<slug>/ and refer
                   to them with source: (needs -o, which the paths are relative to)

In nix_services, source: DIR (a directory with flake.nix) and {file: PATH} in
place of flake, flake_lock or a files entry are read from disk, relative to the
document, and sent inline: the server never sees your files.
`

type schemaClient struct {
	server, token string
	http          *http.Client
}

func schemaCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply" && args[0] != "export" && args[0] != "nix-status") {
		fmt.Fprint(stderr, schemaUsage)
		return 1
	}
	cmd := args[0]
	fs := flag.NewFlagSet("schema "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "-", "")
	server := fs.String("server", os.Getenv("SUPERMATRIX_SERVER"), "")
	token := fs.String("token", os.Getenv("SUPERMATRIX_TOKEN"), "")
	source := fs.String("source", "current", "")
	format := fs.String("format", "yaml", "")
	allowOutside := fs.Bool("allow-outside", false, "")
	wait := fs.Bool("wait", false, "")
	waitTimeout := fs.Duration("wait-timeout", 15*time.Minute, "")
	outFile := fs.String("o", "", "")
	split := fs.String("split", "", "")
	fs.Usage = func() { fmt.Fprint(stderr, schemaUsage) }
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *server == "" || *token == "" {
		fmt.Fprintln(stderr, "error: -server and -token (or $SUPERMATRIX_SERVER and $SUPERMATRIX_TOKEN) are required")
		return 1
	}
	c := &schemaClient{server: strings.TrimRight(*server, "/"), token: *token, http: &http.Client{Timeout: 2 * time.Minute}}

	// The schema status route doubles as the feature check: a server from
	// before schemas doesn't have it.
	if _, err := c.do("GET", "/v1/schema", nil, nil); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			err = errors.New("this server doesn't support schemas yet: update it")
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	if cmd == "nix-status" {
		rows, err := c.nixStatus()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		printNixStatus(stdout, rows)
		return 0
	}

	if cmd == "export" {
		q := url.Values{}
		if *source == "applied" {
			q.Set("source", "applied")
		}
		if *format == "json" || *split != "" {
			q.Set("format", "json")
		}
		if *split != "" && *outFile == "" {
			fmt.Fprintln(stderr, "error: -split needs -o FILE (its source: paths are relative to it)")
			return 1
		}
		out, err := c.do("GET", "/v1/schema/export?"+q.Encode(), nil, nil)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if *split != "" {
			if out, err = splitExport(out, *split, filepath.Dir(*outFile)); err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return 1
			}
		}
		if *outFile != "" {
			if err := os.WriteFile(*outFile, out, 0o644); err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return 1
			}
			return 0
		}
		_, _ = stdout.Write(out)
		return 0
	}

	var doc []byte
	var err error
	base := "."
	if *file == "-" {
		doc, err = io.ReadAll(stdin)
	} else {
		doc, err = os.ReadFile(*file)
		base = filepath.Dir(*file)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if doc, _, err = resolveNixFiles(doc, base, *allowOutside); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	var plan cliPlan
	out, err := c.do("POST", "/v1/schema/plan", doc, nil)
	if err != nil {
		printSchemaError(stderr, err)
		return 1
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	plan.print(stdout)
	if cmd == "plan" {
		if plan.Changed {
			return 2
		}
		return 0
	}
	if plan.Changed {
		// Apply exactly what was printed: if anything changed since, the
		// server refuses.
		out, err = c.do("POST", "/v1/schema/apply", doc, map[string]string{"X-Plan-Hash": plan.PlanHash})
		if err != nil {
			printSchemaError(stderr, err)
			return 1
		}
		var res struct{ Revision int }
		_ = json.Unmarshal(out, &res)
		fmt.Fprintf(stdout, "Applied: schema rev %d.\n", res.Revision)
	}
	if *wait {
		fmt.Fprintln(stdout, "Waiting for the Nix services:")
		if err := c.waitNix(stdout, *waitTimeout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintln(stdout, "All Nix services are where they should be.")
	}
	return 0
}

type httpError struct {
	status int
	body   []byte
}

func (e *httpError) Error() string {
	var b struct{ Error string }
	if json.Unmarshal(e.body, &b) == nil && b.Error != "" {
		return b.Error
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, bytes.TrimSpace(e.body))
}

func (c *schemaClient) do(method, path string, body []byte, hdr map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, c.server+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/yaml")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, &httpError{status: res.StatusCode, body: out}
	}
	return out, nil
}

// printSchemaError prints every problem a 400 lists, one per line.
func printSchemaError(w io.Writer, err error) {
	var he *httpError
	var b struct {
		Errors []struct{ Path, Msg string }
	}
	if errors.As(err, &he) && json.Unmarshal(he.body, &b) == nil && len(b.Errors) > 0 {
		fmt.Fprintln(w, "error: the document has problems:")
		for _, e := range b.Errors {
			if e.Path == "" {
				fmt.Fprintf(w, "  %s\n", e.Msg)
			} else {
				fmt.Fprintf(w, "  %s: %s\n", e.Path, e.Msg)
			}
		}
		return
	}
	fmt.Fprintln(w, "error:", err)
}

type cliPlan struct {
	Ops []struct {
		Action, Kind, Key string
		Drift             bool
		Changes           []struct {
			Field    string
			From, To json.RawMessage
		}
	}
	Changed                   bool
	Creates, Updates, Deletes int
	PlanHash                  string
	Warnings                  []struct{ Path, Msg string }
}

// nixNote says what a Nix service change does on its relays.
func nixNote(kind, action string, fields []string) string {
	switch {
	case kind == "applied_nix_service" && action == "create":
		return " (builds and starts it there)"
	case kind == "applied_nix_service" && action == "delete":
		return " (stops it and tears it down there)"
	case kind == "nix_service" && action == "delete":
		return " (tears it down on every relay)"
	case kind != "nix_service" || action != "update":
		return ""
	}
	for _, f := range fields {
		switch f {
		case "flake", "flake_lock", "files", "run", "attr":
			return " (rebuilds where it runs; restarts only if the output differs)"
		}
	}
	for _, f := range fields {
		switch f {
		case "command", "env", "relay_env", "restart":
			return " (restarts where it runs)"
		}
	}
	return ""
}

func (p cliPlan) print(w io.Writer) {
	marks := map[string]string{"create": "+", "update": "~", "adopt": "=", "delete": "-"}
	for _, op := range p.Ops {
		note := ""
		var fields []string
		for _, ch := range op.Changes {
			fields = append(fields, ch.Field)
		}
		switch {
		case op.Drift:
			note = " (changed outside the schema; reverting)"
		case op.Action == "adopt":
			note = " (taking charge of it)"
		default:
			note = nixNote(op.Kind, op.Action, fields)
		}
		fmt.Fprintf(w, "%s %s %s %s%s\n", marks[op.Action], op.Action, strings.ReplaceAll(op.Kind, "_", " "), op.Key, note)
		for _, ch := range op.Changes {
			fmt.Fprintf(w, "    %s: %s → %s\n", ch.Field, rawOr(ch.From), rawOr(ch.To))
		}
	}
	switch {
	case len(p.Ops) > 0:
		fmt.Fprintf(w, "Plan: %d to create, %d to change, %d to delete.\n", p.Creates, p.Updates, p.Deletes)
	case p.Changed:
		fmt.Fprintln(w, "No resources change; applying records the new document (what it manages may differ).")
	default:
		fmt.Fprintln(w, "No changes: the server matches the document.")
	}
	for _, wn := range p.Warnings {
		fmt.Fprintf(w, "warning: %s: %s\n", wn.Path, wn.Msg)
	}
}

func rawOr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(none)"
	}
	return string(raw)
}
