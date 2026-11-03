package e2etest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// relayAPI calls /v1/relay/* as the relay with token tok.
func (e *env) relayAPI(tok, method, path string, body any, want int, out any) string {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	hdr := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + tok}}
	res := e.req(method, "127.0.0.1", path, rd, hdr)
	b := readBody(e.t, res)
	if res.StatusCode != want {
		e.t.Fatalf("relay %s %s: got %d want %d: %s", method, path, res.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(b), out); err != nil {
			e.t.Fatalf("relay %s %s: %v: %s", method, path, err, b)
		}
	}
	return b
}

func TestRelayManagesItsDeployments(t *testing.T) {
	e := newEnv(t)
	homeID, stopHome := e.startRelay("home")
	defer stopHome()
	homeTok := e.e2eRelayToken
	otherID, stopOther := e.startRelay("other")
	defer stopOther()
	otherTok := e.e2eRelayToken
	th, tp := target(t, false)

	var theirs dep
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": otherID, "subdomain": "theirs", "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &theirs)

	// Unknown tokens are refused; known ones can always ask who they are.
	e.relayAPI("smr_nope", "GET", "/v1/relay/me", nil, 401, nil)
	var me struct {
		ID                string
		ManageDeployments bool
	}
	e.relayAPI(homeTok, "GET", "/v1/relay/me", nil, 200, &me)
	if me.ID != homeID || me.ManageDeployments {
		t.Fatalf("me: %+v", me)
	}

	// Off by default.
	if b := e.relayAPI(homeTok, "GET", "/v1/relay/deployments", nil, 403, nil); !strings.Contains(b, "in the app") {
		t.Fatal(b)
	}
	e.relayAPI(homeTok, "POST", "/v1/relay/deployments", map[string]any{"subdomain": "x", "targetHost": th, "targetPort": tp}, 403, nil)

	// Turned on in the app.
	var r struct{ ManageDeployments bool }
	e.api("PATCH", "/v1/relays/"+homeID, map[string]any{"manageDeployments": true}, 200, &r)
	if !r.ManageDeployments {
		t.Fatal("not turned on")
	}

	// Create: lands on the relay itself, whatever relayId says.
	var mine struct {
		dep
		RelayID string
	}
	e.relayAPI(homeTok, "POST", "/v1/relay/deployments", map[string]any{
		"subdomain": "mine", "targetHost": th, "targetPort": tp, "authMode": "none",
	}, 201, &mine)
	if mine.RelayID != homeID || mine.Host != "mine.test" {
		t.Fatalf("created %+v", mine)
	}
	e.relayAPI(homeTok, "POST", "/v1/relay/deployments", map[string]any{
		"relayId": otherID, "subdomain": "sneaky", "targetHost": th, "targetPort": tp,
	}, 403, nil)

	// It's served.
	if res, body := e.get("mine.test", "/hi", nil); res.StatusCode != 200 || !strings.Contains(body, `"path":"/hi"`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}

	// Read: only its own.
	var list []dep
	e.relayAPI(homeTok, "GET", "/v1/relay/deployments", nil, 200, &list)
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("list %+v", list)
	}
	e.relayAPI(homeTok, "GET", "/v1/relay/deployments/"+theirs.ID, nil, 404, nil)
	e.relayAPI(homeTok, "GET", "/v1/relay/deployments/"+mine.ID, nil, 200, nil)

	// Update: its own, but not onto another relay; never another's.
	e.relayAPI(homeTok, "PATCH", "/v1/relay/deployments/"+mine.ID, map[string]any{"enabled": false}, 200, nil)
	e.relayAPI(homeTok, "PATCH", "/v1/relay/deployments/"+mine.ID, map[string]any{"relayId": otherID}, 403, nil)
	e.relayAPI(homeTok, "PATCH", "/v1/relay/deployments/"+theirs.ID, map[string]any{"enabled": false}, 404, nil)
	// Nor take another's hostname.
	e.relayAPI(homeTok, "PATCH", "/v1/relay/deployments/"+mine.ID, map[string]any{"subdomain": "theirs"}, 400, nil)

	// Tokens: on its own deployments only.
	var tok struct{ ID, Token string }
	e.relayAPI(homeTok, "POST", "/v1/relay/deployments/"+mine.ID+"/tokens", map[string]any{"name": "ci"}, 201, &tok)
	e.relayAPI(homeTok, "POST", "/v1/relay/deployments/"+theirs.ID+"/tokens", map[string]any{"name": "ci"}, 404, nil)
	var theirTok struct{ ID string }
	e.api("POST", "/v1/deployments/"+theirs.ID+"/tokens", map[string]any{"name": "x"}, 201, &theirTok)
	e.relayAPI(homeTok, "DELETE", "/v1/relay/deployments/"+theirs.ID+"/tokens/"+theirTok.ID, nil, 404, nil)
	e.relayAPI(homeTok, "DELETE", "/v1/relay/deployments/"+mine.ID+"/tokens/"+theirTok.ID, nil, 404, nil)
	e.relayAPI(homeTok, "DELETE", "/v1/relay/deployments/"+mine.ID+"/tokens/"+tok.ID, nil, 200, nil)

	// Delete: its own only.
	e.relayAPI(homeTok, "DELETE", "/v1/relay/deployments/"+theirs.ID, nil, 404, nil)
	e.relayAPI(homeTok, "DELETE", "/v1/relay/deployments/"+mine.ID, nil, 200, nil)
	e.api("GET", "/v1/deployments/"+theirs.ID, nil, 200, nil)

	// The other relay was never allowed.
	e.relayAPI(otherTok, "GET", "/v1/relay/deployments", nil, 403, nil)
	// A relay token isn't an admin session.
	e.relayAPI(homeTok, "GET", "/v1/deployments", nil, 401, nil)

	// Turned off again: refused at once.
	e.api("PATCH", "/v1/relays/"+homeID, map[string]any{"manageDeployments": false}, 200, nil)
	e.relayAPI(homeTok, "GET", "/v1/relay/deployments", nil, 403, nil)
	e.relayAPI(homeTok, "GET", "/v1/relay/domains", nil, 403, nil)
}
