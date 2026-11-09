package main

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"main/database"
)

func testDB(t *testing.T) (*gorm.DB, *database.Deployment) {
	t.Helper()
	db, err := database.Connect(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	relay := database.Relay{ID: database.NewID(), Name: "r1"}
	if err := db.Create(&relay).Error; err != nil {
		t.Fatal(err)
	}
	host := "app.test"
	d := database.Deployment{
		ID: database.NewID(), RelayID: relay.ID, Host: &host, Protocol: "http",
		TargetHost: "127.0.0.1", TargetPort: 8080, AuthMode: "none",
		RateLimitRequests: 100, RateLimitPeriodS: 60,
	}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	return db, &d
}

func TestRateLimitCmd(t *testing.T) {
	db, d := testDB(t)

	rateLimitCmd(db, []string{"app.test", "on", "50", "60", "10"})
	var got database.Deployment
	if err := db.First(&got, "id = ?", d.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.RateLimitEnabled || got.RateLimitRequests != 50 || got.RateLimitPeriodS != 60 || got.RateLimitBurst != 10 {
		t.Fatalf("after on: %+v", got)
	}

	rateLimitCmd(db, []string{"app.test", "off"})
	if err := db.First(&got, "id = ?", d.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.RateLimitEnabled {
		t.Fatalf("after off: %+v", got)
	}
}

func TestPathRulesCmd(t *testing.T) {
	db, d := testDB(t)

	pathRulesCmd(db, []string{"app.test", "add", "/secret", "10.0.0.0/8"})
	var rules []database.PathRule
	db.Where("deployment_id = ?", d.ID).Find(&rules)
	if len(rules) != 1 || rules[0].PathPrefix != "/secret" || rules[0].IPAllowlist != "10.0.0.0/8" {
		t.Fatalf("after add: %+v", rules)
	}

	pathRulesCmd(db, []string{"app.test", "set", "/secret", "10.0.0.0/8", "192.168.1.5"})
	db.Where("deployment_id = ?", d.ID).Find(&rules)
	if len(rules) != 1 || rules[0].IPAllowlist != "10.0.0.0/8\n192.168.1.5" {
		t.Fatalf("after set: %+v", rules)
	}

	pathRulesCmd(db, []string{"app.test", "remove", "/secret"})
	db.Where("deployment_id = ?", d.ID).Find(&rules)
	if len(rules) != 0 {
		t.Fatalf("after remove: %+v", rules)
	}
}

func TestNormalizePathPrefix(t *testing.T) {
	for in, wantOK := range map[string]bool{
		"/secret":  true,
		"/":        true,
		"no-slash": false,
		"/trail/":  false,
		"/has sp":  false,
	} {
		_, err := normalizePathPrefix(in)
		if (err == nil) != wantOK {
			t.Errorf("%q: err=%v, want ok=%v", in, err, wantOK)
		}
	}
}
