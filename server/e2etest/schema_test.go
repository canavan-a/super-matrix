package e2etest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type planOut struct {
	Ops []struct {
		Action, Kind, Key string
		Drift             bool
		Changes           []struct{ Field string }
	}
	Changed                   bool
	Creates, Updates, Deletes int
	PlanHash                  string
	BaseRevision              int
}

// schemaReq sends a document (or nothing) to a schema route with token.
func (e *env) schemaReq(method, path, token, body string, want int) string {
	e.t.Helper()
	hdr := http.Header{"Content-Type": {"application/yaml"}}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	res := e.req(method, "127.0.0.1", path, strings.NewReader(body), hdr)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		e.t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, b)
	}
	return string(b)
}

func (e *env) plan(token, doc string) planOut {
	e.t.Helper()
	var p planOut
	if err := json.Unmarshal([]byte(e.schemaReq("POST", "/v1/schema/plan", token, doc, 200)), &p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (p planOut) has(action, kind, key string) bool {
	for _, op := range p.Ops {
		if op.Action == action && op.Kind == kind && op.Key == key {
			return true
		}
	}
	return false
}

type managedDep struct {
	ID          string
	TargetPort  int
	Enabled     bool
	IPAllowlist []string
	Managed     *struct {
		Revision int
		Fields   []string
		Entries  map[string][]string
	}
	PathRules []struct {
		ID         string
		PathPrefix string
	}
}

func TestSchema(t *testing.T) {
	e := newEnv(t)
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	// A deployment made in the app before any schema.
	var manual managedDep
	e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "manual", "targetHost": th, "targetPort": tp, "authMode": "none"}, 201, &manual)

	var tok struct {
		APIToken struct{ ID string }
		Token    string
	}
	e.api("POST", "/v1/api-tokens", map[string]any{"name": "ci"}, 201, &tok)
	if !strings.HasPrefix(tok.Token, "sma_") {
		t.Fatalf("token: %+v", tok)
	}
	ci := tok.Token

	t.Run("auth", func(t *testing.T) {
		e.schemaReq("GET", "/v1/schema", "", "", 401)
		e.schemaReq("GET", "/v1/schema", "sma_nope", "", 401)
		e.schemaReq("GET", "/v1/schema", e.token, "", 200)
		e.schemaReq("GET", "/v1/schema", ci, "", 200)
		// An API token is only for the schema.
		e.schemaReq("GET", "/v1/deployments", ci, "", 401)
		e.api("POST", "/v1/api-tokens", map[string]any{"name": "x", "scope": "everything"}, 400, nil)
	})

	doc := fmt.Sprintf(`version: 1
domains: ["*.test", "..."]
groups:
  ip:
    office: {entries: [127.0.0.1]}
  email:
    team: {entries: [ann@example.com]}
deployments:
  "...": true
  app.test:
    relay: pi
    target_host: %s
    target_port: %d
    auth_mode: none
    ip_groups: [office]
    ip_allowlist: ["..."]
    path_rules:
      /secret: {ip_allowlist: [10.9.9.9]}
    ui: [enabled]
`, th, tp)

	t.Run("errors", func(t *testing.T) {
		out := e.schemaReq("POST", "/v1/schema/plan", ci, strings.Replace(doc, "relay: pi", "relay: nope", 1), 400)
		if !strings.Contains(out, `"path":"deployments.app.test.relay"`) {
			t.Fatal(out)
		}
		out = e.schemaReq("POST", "/v1/schema/plan", ci, strings.Replace(doc, "target_port", "target_prot", 1), 400)
		if !strings.Contains(out, "target_prot: unknown field") {
			t.Fatal(out)
		}
		e.schemaReq("POST", "/v1/schema/plan", ci, "version: 1\ndeployments: [", 400)
		// Nothing was changed by those plans.
		e.api("GET", "/v1/ip-groups", nil, 200, nil)
		var gs []ipGroup
		e.api("GET", "/v1/ip-groups", nil, 200, &gs)
		if len(gs) != 0 {
			t.Fatalf("a plan left groups behind: %+v", gs)
		}
	})

	p := e.plan(ci, doc)
	if !p.Changed || !p.has("create", "deployment", "app.test") || !p.has("create", "path_rule", "app.test/secret") ||
		!p.has("create", "ip_group", "office") || !p.has("adopt", "domain", "*.test") || p.Deletes != 0 || p.has("delete", "deployment", "manual.test") {
		t.Fatalf("plan: %+v", p)
	}
	// Planning made nothing.
	e.api("GET", "/v1/deployments", nil, 200, nil)

	e.schemaReq("POST", "/v1/schema/apply?planHash=stale", ci, doc, 409)
	var applied struct {
		Applied  bool
		Revision int
	}
	json.Unmarshal([]byte(e.schemaReq("POST", "/v1/schema/apply?planHash="+p.PlanHash, ci, doc, 200)), &applied)
	if !applied.Applied || applied.Revision != 1 {
		t.Fatalf("apply: %+v", applied)
	}

	res, _ := e.get("app.test", "/", nil)
	if res.StatusCode != 200 {
		t.Fatalf("app.test: %d", res.StatusCode)
	}
	if res, _ := e.get("app.test", "/secret/x", nil); res.StatusCode != 403 {
		t.Fatalf("path rule: %d", res.StatusCode)
	}

	var deps []managedDep
	e.api("GET", "/v1/deployments", nil, 200, &deps)
	var app managedDep
	for _, d := range deps {
		if d.ID != manual.ID {
			app = d
		}
	}
	if app.Managed == nil || app.Managed.Revision != 1 || !slices.Contains(app.Managed.Fields, "target_port") ||
		slices.Contains(app.Managed.Fields, "enabled") || slices.Contains(app.Managed.Fields, "ip_allowlist") {
		t.Fatalf("managed: %+v", app.Managed)
	}
	for _, d := range deps {
		if d.ID == manual.ID && d.Managed != nil {
			t.Fatal("the app's own deployment shows as managed")
		}
	}

	t.Run("locks", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+app.ID, map[string]any{"targetPort": tp + 1}, 409, nil)
		// Resending a managed value as it is goes through, as do ui fields
		// and additions to a spread list.
		e.api("PATCH", "/v1/deployments/"+app.ID, map[string]any{"targetPort": tp, "enabled": false}, 200, nil)
		e.api("PATCH", "/v1/deployments/"+app.ID, map[string]any{"enabled": true, "ipAllowlist": []string{"10.1.0.0/16"}}, 200, nil)
		e.api("PATCH", "/v1/deployments/"+app.ID, map[string]any{"subdomain": "renamed"}, 409, nil)
		e.api("DELETE", "/v1/deployments/"+app.ID, nil, 409, nil)
		e.api("POST", "/v1/deployments/"+app.ID+"/path-rules", map[string]any{"pathPrefix": "/x"}, 409, nil)
		e.api("DELETE", "/v1/deployments/"+app.ID+"/path-rules/"+app.PathRules[0].ID, nil, 409, nil)
		// The deployments collection is spread: the app can add more.
		e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "extra", "targetHost": th, "targetPort": tp, "authMode": "none"}, 201, nil)
		// IP groups are listed in full: the app can't add one.
		e.api("POST", "/v1/ip-groups", map[string]any{"name": "other", "entries": []string{"10.0.0.1"}}, 409, nil)
		var gs []ipGroup
		e.api("GET", "/v1/ip-groups", nil, 200, &gs)
		e.api("PATCH", "/v1/ip-groups/"+gs[0].ID, map[string]any{"entries": []string{"127.0.0.2"}}, 409, nil)
		e.api("DELETE", "/v1/ip-groups/"+gs[0].ID, nil, 409, nil)
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"slug": "pi2"}, 409, nil)
		e.api("PATCH", "/v1/relays/"+relayID, map[string]any{"name": "Pi"}, 200, nil)
	})

	t.Run("reapply keeps the app's extras", func(t *testing.T) {
		p := e.plan(ci, doc)
		if p.Changed || len(p.Ops) != 0 {
			t.Fatalf("plan after apply: %+v", p)
		}
		var d managedDep
		e.api("GET", "/v1/deployments/"+app.ID, nil, 200, &d)
		if strings.Join(d.IPAllowlist, ",") != "10.1.0.0/16" {
			t.Fatalf("allow list: %v", d.IPAllowlist)
		}
	})

	t.Run("override and drift", func(t *testing.T) {
		var ov struct{ On bool }
		e.api("PUT", "/v1/settings/schema-override", map[string]any{"on": true, "minutes": 5}, 200, &ov)
		if !ov.On {
			t.Fatal("override off")
		}
		e.api("PATCH", "/v1/deployments/"+app.ID, map[string]any{"targetPort": tp + 1}, 200, nil)
		e.api("PUT", "/v1/settings/schema-override", map[string]any{"on": false}, 200, &ov)
		p := e.plan(ci, doc)
		if len(p.Ops) != 1 || p.Ops[0].Action != "update" || !p.Ops[0].Drift || p.Ops[0].Changes[0].Field != "target_port" {
			t.Fatalf("drift: %+v", p)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, doc, 200)
		var d managedDep
		e.api("GET", "/v1/deployments/"+app.ID, nil, 200, &d)
		if d.TargetPort != tp || d.Managed.Revision != 2 {
			t.Fatalf("after reverting drift: %+v", d)
		}
	})

	t.Run("dropping from the document", func(t *testing.T) {
		smaller := strings.Split(doc, "deployments:")[0] + "deployments:\n  \"...\": true\n"
		p := e.plan(ci, smaller)
		if !p.has("delete", "deployment", "app.test") || p.Deletes != 1 {
			t.Fatalf("plan: %+v", p)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, smaller, 200)
		var deps []managedDep
		e.api("GET", "/v1/deployments", nil, 200, &deps)
		if len(deps) != 2 {
			t.Fatalf("the app's deployments should stay: %+v", deps)
		}
	})

	t.Run("export round trip", func(t *testing.T) {
		exported := e.schemaReq("GET", "/v1/schema/export", ci, "", 200)
		if strings.Contains(exported, "...") || !strings.Contains(exported, "manual.test") {
			t.Fatal(exported)
		}
		e.schemaReq("POST", "/v1/schema/apply", ci, exported, 200)
		if p := e.plan(ci, exported); len(p.Ops) != 0 || p.Changed {
			t.Fatalf("export isn't a fixed point: %+v\n%s", p, exported)
		}
		// Every collection is now listed in full.
		e.api("POST", "/v1/deployments", map[string]any{"relayId": relayID, "subdomain": "more", "targetHost": th, "targetPort": tp}, 409, nil)
		applied := e.schemaReq("GET", "/v1/schema/export?source=applied", ci, "", 200)
		if applied != exported {
			t.Fatal("source=applied isn't the document as sent")
		}
		e.schemaReq("GET", "/v1/schema/jsonschema", ci, "", 200)
	})

	t.Run("revoked token", func(t *testing.T) {
		e.api("DELETE", "/v1/api-tokens/"+tok.APIToken.ID, nil, 200, nil)
		e.schemaReq("GET", "/v1/schema", ci, "", 401)
	})
}

func TestSchemaBridgesAndEmailGroups(t *testing.T) {
	e := newEnv(t)
	e.bridgeRelay("edge", nil)
	e.bridgeRelay("home", nil)
	th, tp := target(t, false)
	listen := freePort(t, "tcp")

	doc := fmt.Sprintf(`version: 1
groups:
  email:
    team: {entries: [ann@example.com]}
deployments:
  otp.test:
    relay: home
    target_host: %s
    target_port: %d
    auth_mode: otp
    email_groups: [team]
bridges:
  db:
    from_relay: edge
    to_relay: home
    listen_port: %d
    target_host: 127.0.0.1
    target_port: 5432
`, th, tp, listen)

	// A failing item makes the whole apply fail: nothing is made.
	bad := strings.Replace(doc, "to_relay: home", "to_relay: nowhere", 1)
	out := e.schemaReq("POST", "/v1/schema/apply", e.token, bad, 400)
	if !strings.Contains(out, "bridges.db.to_relay") {
		t.Fatal(out)
	}
	var deps []managedDep
	e.api("GET", "/v1/deployments", nil, 200, &deps)
	if len(deps) != 0 {
		t.Fatalf("a failed apply left %d deployments", len(deps))
	}

	e.schemaReq("POST", "/v1/schema/apply", e.token, doc, 200)
	var bs []struct {
		ID, Slug, Name string
		ListenPort     int
		Managed        *struct{ Fields []string }
	}
	e.api("GET", "/v1/bridges", nil, 200, &bs)
	if len(bs) != 1 || bs[0].Slug != "db" || bs[0].Name != "db" || bs[0].ListenPort != listen || bs[0].Managed == nil {
		t.Fatalf("bridges: %+v", bs)
	}
	e.api("PATCH", "/v1/bridges/"+bs[0].ID, map[string]any{"targetPort": 5433}, 409, nil)
	e.api("PATCH", "/v1/bridges/"+bs[0].ID, map[string]any{"slug": "database"}, 409, nil)
	e.api("PATCH", "/v1/bridges/"+bs[0].ID, map[string]any{"name": "db"}, 200, nil)
	e.api("DELETE", "/v1/bridges/"+bs[0].ID, nil, 409, nil)

	var info struct {
		Features       []string
		SchemaVersions struct{ Min, Current int }
	}
	e.api("GET", "/v1/info", nil, 200, &info)
	if !slices.Contains(info.Features, "schema") || !slices.Contains(info.Features, "api-tokens") || info.SchemaVersions.Current != 1 {
		t.Fatalf("info: %+v", info)
	}

	exported := e.schemaReq("GET", "/v1/schema/export", e.token, "", 200)
	if !strings.Contains(exported, "team") || !strings.Contains(exported, "from_relay: edge") {
		t.Fatal(exported)
	}
	// Applying the export only adopts what the schema didn't manage yet
	// (the seeded domain); it changes nothing.
	p := e.plan(e.token, exported)
	for _, op := range p.Ops {
		if op.Action != "adopt" || len(op.Changes) > 0 {
			t.Fatalf("export plan: %+v\n%s", p, exported)
		}
	}
	if !p.has("adopt", "domain", "*.test") {
		t.Fatalf("export plan: %+v", p)
	}
}

func TestSchemaMarkers(t *testing.T) {
	e := newEnv(t)
	_, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)
	tok := e.token

	doc := func(allow, groups, extraGroup string) string {
		return fmt.Sprintf(`version: 1
groups:
  ip:
    office: {entries: [10.0.0.1]}
%s
deployments:
  a.test:
    relay: pi
    target_host: %s
    target_port: %d
    auth_mode: none
    enabled: false
    ip_allowlist: [%s, "..."]
    ip_groups: [%s]
    ui: [enabled]
`, extraGroup, th, tp, allow, groups)
	}
	e.schemaReq("POST", "/v1/schema/apply", tok, doc("10.0.0.1", "office, lab", "    lab: {entries: [10.5.0.0/16]}"), 200)

	var deps []struct {
		ID          string
		Enabled     bool
		IPAllowlist []string
		IPGroups    []string
	}
	e.api("GET", "/v1/deployments", nil, 200, &deps)
	if len(deps) != 1 || deps[0].Enabled || len(deps[0].IPGroups) != 2 {
		t.Fatalf("created: %+v", deps)
	}
	id := deps[0].ID

	// The app turns it on (ui) and adds to the spread allow list.
	e.api("PATCH", "/v1/deployments/"+id, map[string]any{"enabled": true, "ipAllowlist": []string{"10.0.0.1", "10.2.0.0/16"}}, 200, nil)
	// The schema's own entry can't be taken off.
	e.api("PATCH", "/v1/deployments/"+id, map[string]any{"ipAllowlist": []string{"10.2.0.0/16"}}, 409, nil)

	// Swapping the schema's entry and dropping a group (still listed by
	// the deployment until this same apply) keeps the app's entry and its
	// ui value.
	e.schemaReq("POST", "/v1/schema/apply", tok, doc("10.0.0.2", "office", ""), 200)
	e.api("GET", "/v1/deployments", nil, 200, &deps)
	if !deps[0].Enabled || strings.Join(deps[0].IPAllowlist, ",") != "10.2.0.0/16,10.0.0.2/32" || len(deps[0].IPGroups) != 1 {
		t.Fatalf("after second apply: %+v", deps[0])
	}
	var gs []ipGroup
	e.api("GET", "/v1/ip-groups", nil, 200, &gs)
	if len(gs) != 1 || gs[0].Name != "office" {
		t.Fatalf("groups: %+v", gs)
	}

	// A collection the document leaves out is left alone, even after a
	// schema listed it.
	e.schemaReq("POST", "/v1/schema/apply", tok, "version: 1\n", 200)
	e.api("GET", "/v1/deployments", nil, 200, &deps)
	if len(deps) != 1 {
		t.Fatalf("an empty document deleted things: %+v", deps)
	}
	// …and is no longer locked.
	e.api("PATCH", "/v1/deployments/"+id, map[string]any{"targetPort": tp + 1}, 200, nil)
	// An empty collection, though, deletes everything in it.
	p := e.plan(tok, "version: 1\ndeployments: {}\n")
	if p.Deletes != 1 {
		t.Fatalf("plan: %+v", p)
	}
}

// Fixes from review: defaults that come from the app's values, and
// renaming groups the schema names but doesn't own.
func TestSchemaAppDefaults(t *testing.T) {
	e := newEnv(t)
	e.bridgeRelay("edge", nil)
	e.bridgeRelay("home", nil)
	listen := freePort(t, "tcp")
	var g struct{ ID string }
	e.api("POST", "/v1/ip-groups", map[string]any{"name": "office", "entries": []string{"10.0.0.0/8"}}, 201, &g)

	doc := fmt.Sprintf(`version: 1
deployments:
  ssh.test:
    relay: home
    protocol: ssh
    target_host: 127.0.0.1
    ip_groups: [office]
bridges:
  db:
    from_relay: edge
    to_relay: home
    listen_port: %d
    target_host: 127.0.0.1
`, listen)
	e.schemaReq("POST", "/v1/schema/apply", e.token, doc, 200)

	// protocol and listen_port handed to the app: the defaults of
	// target_port follow the app's values, so nothing changes.
	loose := strings.Replace(doc, "    protocol: ssh\n", "    ui: [protocol]\n", 1)
	loose = strings.Replace(loose, fmt.Sprintf("    listen_port: %d\n", listen), "    ui: [listen_port]\n", 1)
	if p := e.plan(e.token, loose); len(p.Ops) != 0 {
		t.Fatalf("plan: %+v", p)
	}
	// A bridge target_host the schema manages has to be given.
	out := e.schemaReq("POST", "/v1/schema/plan", e.token, strings.Replace(doc, "    target_host: 127.0.0.1\n", "", 2), 400)
	if !strings.Contains(out, "bridges.db.target_host") {
		t.Fatal(out)
	}

	// The schema names office in an allow list: it keeps that name.
	e.api("PATCH", "/v1/ip-groups/"+g.ID, map[string]any{"name": "hq"}, 409, nil)
	e.api("PATCH", "/v1/ip-groups/"+g.ID, map[string]any{"name": "Office"}, 200, nil)
	e.api("PATCH", "/v1/ip-groups/"+g.ID, map[string]any{"entries": []string{"10.1.0.0/16"}}, 200, nil)
}
