package e2etest

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

type emailGroup struct {
	ID          string
	Name        string
	Entries     []string
	Deployments int
}

type depEmailGroups struct {
	ID          string
	OTPEmails   []string `json:"otpEmails"`
	EmailGroups []string `json:"emailGroups"`
}

func TestEmailGroups(t *testing.T) {
	e := newEnv(t)
	mail := &fakeMail{codes: map[string]string{}}
	e.edge.Mail = mail
	relayID, stop := e.startRelay("pi")
	defer stop()
	th, tp := target(t, false)

	var info struct{ Features []string }
	e.api("GET", "/v1/info", nil, 200, &info)
	if !slices.Contains(info.Features, "email-groups") {
		t.Fatalf("features: %v", info.Features)
	}

	var team emailGroup
	e.api("POST", "/v1/email-groups", map[string]any{"name": " Team ", "entries": []string{"Ann@Example.com", "ann@example.com", "*@corp.example"}}, 201, &team)
	if team.Name != "Team" || !slices.Equal(team.Entries, []string{"ann@example.com", "*@corp.example"}) {
		t.Fatalf("created: %+v", team)
	}

	t.Run("validation", func(t *testing.T) {
		e.api("POST", "/v1/email-groups", map[string]any{"name": "team", "entries": []string{"x@example.com"}}, 409, nil)
		e.api("POST", "/v1/email-groups", map[string]any{"name": "empty", "entries": []string{}}, 400, nil)
		e.api("POST", "/v1/email-groups", map[string]any{"name": "bad", "entries": []string{"not an email"}}, 400, nil)
		e.api("POST", "/v1/email-groups", map[string]any{"name": "bad", "entries": []string{"a*b@example.com"}}, 400, nil)
		e.api("POST", "/v1/email-groups", map[string]any{"name": "", "entries": []string{"x@example.com"}}, 400, nil)
		e.api("PATCH", "/v1/email-groups/"+team.ID, map[string]any{"entries": []string{}}, 400, nil)
		e.api("POST", "/v1/deployments", map[string]any{
			"relayId": relayID, "subdomain": "nogroup", "targetHost": th, "targetPort": tp, "authMode": "otp", "emailGroups": []string{"nope"},
		}, 400, nil)
	})

	// A group alone is enough for email codes.
	var d depEmailGroups
	e.api("POST", "/v1/deployments", map[string]any{
		"relayId": relayID, "subdomain": "otp", "targetHost": th, "targetPort": tp, "authMode": "otp",
		"emailGroups": []string{team.ID, team.ID},
	}, 201, &d)
	if !slices.Equal(d.EmailGroups, []string{team.ID}) || len(d.OTPEmails) != 0 {
		t.Fatalf("deployment: %+v", d)
	}

	t.Run("in use", func(t *testing.T) {
		var g emailGroup
		e.api("GET", "/v1/email-groups/"+team.ID, nil, 200, &g)
		if g.Deployments != 1 {
			t.Fatalf("use count: %+v", g)
		}
		e.api("DELETE", "/v1/email-groups/"+team.ID, nil, 409, nil)
		// Dropping the last group with no addresses of its own leaves email
		// codes with nobody to send to.
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"emailGroups": []string{}}, 400, nil)
		// An update that doesn't touch the groups keeps them, and counts them.
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"sessionTtlS": 900}, 200, &d)
		if !slices.Equal(d.EmailGroups, []string{team.ID}) {
			t.Fatalf("groups lost on update: %+v", d)
		}
	})

	post := func(path string, v url.Values) (int, []string) {
		res := e.req("POST", "otp.test", path, strings.NewReader(v.Encode()), form(v))
		readBody(t, res)
		var cookies []string
		for _, c := range res.Cookies() {
			cookies = append(cookies, c.Name+"="+c.Value)
		}
		return res.StatusCode, cookies
	}
	signIn := func(email string) string {
		t.Helper()
		post("/_supermatrix/login", url.Values{"email": {email}})
		e.waitFor(func() bool { return mail.code(email) != "" })
		status, cookies := post("/_supermatrix/verify", url.Values{"email": {email}, "code": {mail.code(email)}})
		if status != 303 || len(cookies) == 0 {
			t.Fatalf("verify %s: %d", email, status)
		}
		return cookies[0]
	}

	ann := signIn("ann@example.com")
	bob := signIn("bob@corp.example") // by the group's *@corp.example
	get := func(cookie string) int {
		res, _ := e.get("otp.test", "/", map[string][]string{"Cookie": {cookie}})
		return res.StatusCode
	}
	if get(ann) != 200 || get(bob) != 200 {
		t.Fatalf("signed in through the group: ann %d, bob %d", get(ann), get(bob))
	}

	t.Run("taking someone off the group signs them out", func(t *testing.T) {
		e.api("PATCH", "/v1/email-groups/"+team.ID, map[string]any{"entries": []string{"*@corp.example"}}, 200, nil)
		if get(ann) == 200 {
			t.Fatal("ann still signed in after leaving the group")
		}
		if get(bob) != 200 {
			t.Fatalf("bob signed out too: %d", get(bob))
		}
		// Codes go out in the background: ask for ann's, then for someone
		// still listed, and once that one arrives ann's would have too.
		mail.mu.Lock()
		mail.codes = map[string]string{}
		mail.mu.Unlock()
		post("/_supermatrix/login", url.Values{"email": {"ann@example.com"}})
		post("/_supermatrix/login", url.Values{"email": {"cat@corp.example"}})
		e.waitFor(func() bool { return mail.code("cat@corp.example") != "" })
		if mail.code("ann@example.com") != "" {
			t.Fatal("sent a code to someone no longer on the group")
		}
	})

	t.Run("delete once unused", func(t *testing.T) {
		e.api("PATCH", "/v1/deployments/"+d.ID, map[string]any{"otpEmails": []string{"ann@example.com"}, "emailGroups": []string{}}, 200, &d)
		if len(d.EmailGroups) != 0 {
			t.Fatalf("groups after clearing: %+v", d)
		}
		e.api("DELETE", "/v1/email-groups/"+team.ID, nil, 200, nil)
	})
}
