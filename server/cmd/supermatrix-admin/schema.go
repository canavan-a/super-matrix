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
	"strings"
	"time"
)

// schema plan|apply|export talks to a running server over HTTP, unlike the
// other commands here: the server checks the document with the same
// validators the app's edits go through, makes the change in one
// transaction, and puts it into effect at once, without a restart.
//
// Exit codes: 0 success (plan: no changes), 2 plan has changes, 1 error.

const schemaUsage = `usage: supermatrix-admin schema plan|apply|export [flags]

  plan    show what applying the document would change (exit 2 if anything)
  apply   apply the document, printing what it changed
  export  print the server's state as a document (or the last applied one)

flags:
  -f FILE        the document, YAML or JSON (default: standard input)
  -server URL    the server, e.g. https://admin.example.com ($SUPERMATRIX_SERVER)
  -token TOKEN   an API token (sma_…) with the schema scope ($SUPERMATRIX_TOKEN)
  -source SRC    export: current (default) or applied
  -format FMT    export: yaml (default) or json
`

type schemaClient struct {
	server, token string
	http          *http.Client
}

func schemaCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply" && args[0] != "export") {
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

	if cmd == "export" {
		q := url.Values{}
		if *source == "applied" {
			q.Set("source", "applied")
		}
		if *format == "json" {
			q.Set("format", "json")
		}
		out, err := c.do("GET", "/v1/schema/export?"+q.Encode(), nil, nil)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		_, _ = stdout.Write(out)
		return 0
	}

	var doc []byte
	var err error
	if *file == "-" {
		doc, err = io.ReadAll(stdin)
	} else {
		doc, err = os.ReadFile(*file)
	}
	if err != nil {
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
	if !plan.Changed {
		return 0
	}
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
}

func (p cliPlan) print(w io.Writer) {
	marks := map[string]string{"create": "+", "update": "~", "adopt": "=", "delete": "-"}
	for _, op := range p.Ops {
		note := ""
		switch {
		case op.Drift:
			note = " (changed outside the schema; reverting)"
		case op.Action == "adopt":
			note = " (taking charge of it)"
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
}

func rawOr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(none)"
	}
	return string(raw)
}
