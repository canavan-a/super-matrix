package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchemaCmd(t *testing.T) {
	var appliedHash string
	changed := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sma_test" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/schema":
			_, _ = w.Write([]byte(`{"revision":0}`))
		case "/v1/schema/plan":
			if strings.Contains(string(body), "bad") {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"x","errors":[{"path":"deployments.a.relay","msg":"no relay with the slug \"bad\""}]}`))
				return
			}
			if !changed {
				_, _ = w.Write([]byte(`{"ops":[],"changed":false,"planHash":"h0"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ops":[{"action":"update","kind":"deployment","key":"a.test","changes":[{"field":"target_port","from":80,"to":81}]}],"changed":true,"updates":1,"planHash":"h1"}`))
		case "/v1/schema/apply":
			appliedHash = r.Header.Get("X-Plan-Hash")
			_, _ = w.Write([]byte(`{"applied":true,"revision":3}`))
		case "/v1/schema/export":
			_, _ = w.Write([]byte("version: 1\n"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	run := func(stdin string, args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := schemaCmd(append(args, "-server", srv.URL, "-token", "sma_test"), strings.NewReader(stdin), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, out, _ := run("version: 1", "plan"); code != 2 || !strings.Contains(out, "~ update deployment a.test") || !strings.Contains(out, "target_port: 80 → 81") {
		t.Fatalf("plan: %d %s", code, out)
	}
	if code, out, _ := run("version: 1", "apply"); code != 0 || appliedHash != "h1" || !strings.Contains(out, "rev 3") {
		t.Fatalf("apply: %d %q %s", code, appliedHash, out)
	}
	if code, _, errOut := run("bad", "plan"); code != 1 || !strings.Contains(errOut, "deployments.a.relay: no relay") {
		t.Fatalf("errors: %d %s", code, errOut)
	}
	if code, out, _ := run("", "export"); code != 0 || out != "version: 1\n" {
		t.Fatalf("export: %d %s", code, out)
	}
	changed = false
	appliedHash = ""
	if code, _, _ := run("version: 1", "apply"); code != 0 || appliedHash != "" {
		t.Fatal("applied a plan with no changes")
	}
	if code, _, _ := run("version: 1", "plan"); code != 0 {
		t.Fatalf("no-change plan exit %d", code)
	}
	var errOut bytes.Buffer
	if schemaCmd([]string{"plan", "-server", srv.URL, "-token", "sma_wrong"}, strings.NewReader(""), io.Discard, &errOut) != 1 {
		t.Fatal("bad token accepted")
	}
}
