package database

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"main/database/migrations"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Home Lab":                     "home-lab",
		"  home--lab  ":                "home-lab",
		"Pi #2 (garage)":               "pi-2-garage",
		"café":                         "cafe",
		"Zürich Ⅱ":                     "zurich-ii",
		"東京":                           "relay",
		"!!!":                          "relay",
		"":                             "relay",
		"already-a-slug":               "already-a-slug",
		strings.Repeat("a", 70):        strings.Repeat("a", 63),
		strings.Repeat("a", 62) + " b": strings.Repeat("a", 62),
	} {
		if got := Slugify(in, "relay"); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if got := Slugify(in, "relay"); !ValidSlug(got) {
			t.Errorf("Slugify(%q) = %q isn't a valid slug", in, got)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a b", "a_b", strings.Repeat("a", 64)} {
		if ValidSlug(bad) {
			t.Errorf("ValidSlug(%q) = true", bad)
		}
	}
}

// openAt builds a database as the release with migrations up to version
// left it, so the test can add rows the way that release would have.
func openAt(t *testing.T, path string, version int64) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := migrations.UpTo(sqlDB, version); err != nil {
		t.Fatal(err)
	}
	return db
}

func exec(t *testing.T, db *gorm.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func slugs(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var rows []struct{ ID, Slug string }
	if err := db.Table(table).Select("id, slug").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.ID] = r.Slug
	}
	return out
}

// Relays and bridges from before slugs get unique ones made from their
// names, oldest first, when a newer binary starts.
func TestSlugBackfill(t *testing.T) {
	path := t.TempDir() + "/old.db"
	old := openAt(t, path, 11)
	exec(t, old,
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r1', 'Home Lab', 'h1', '2025-01-01')`,
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r2', 'home-lab', 'h2', '2025-01-02')`,
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r3', '???', 'h3', '2025-01-03')`,
		`INSERT INTO bridges (id, name, protocol, listen_port, to_relay_id, target_host, target_port, created_at, updated_at)
		 VALUES ('b1', 'Postgres', 'tcp', 5432, 'r1', 'db', 5432, '2025-01-01', '2025-01-01')`,
	)
	sqlDB, _ := old.DB()
	sqlDB.Close()

	db, err := Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	// Connecting alone (as the admin CLI does) leaves them be.
	if got := slugs(t, db, "relays"); got["r1"] != "" {
		t.Fatalf("Connect wrote slugs: %v", got)
	}
	if err := BackfillSlugs(db); err != nil {
		t.Fatal(err)
	}
	if got := slugs(t, db, "relays"); got["r1"] != "home-lab" || got["r2"] != "home-lab-2" || got["r3"] != "relay" {
		t.Fatalf("relay slugs: %v", got)
	}
	if got := slugs(t, db, "bridges"); got["b1"] != "postgres" {
		t.Fatalf("bridge slugs: %v", got)
	}
}

// An older binary run against a migrated database inserts relays with the
// column's default, an empty slug. That must not trip the unique index,
// and the next newer server's startup fills them in.
func TestSlugDowngrade(t *testing.T) {
	path := t.TempDir() + "/t.db"
	db, err := Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db,
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r1', 'pi', 'h1', CURRENT_TIMESTAMP)`,
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r2', 'pi', 'h2', CURRENT_TIMESTAMP)`,
	)
	sqlDB, _ := db.DB()
	sqlDB.Close()

	db, err = Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := BackfillSlugs(db); err != nil {
		t.Fatal(err)
	}
	got := slugs(t, db, "relays")
	if got["r1"] == "" || got["r2"] == "" || got["r1"] == got["r2"] {
		t.Fatalf("relay slugs after restart: %v", got)
	}
	if err := db.Create(&Relay{ID: "r3", Name: "x", Slug: got["r1"], TokenHash: "h3", CreatedAt: time.Now()}).Error; err == nil {
		t.Fatal("a duplicate slug was accepted")
	}
}

// Every migration's Down undoes its Up: rolling all the way back and up
// again works on a database with rows in it.
func TestMigrationsDownAndUp(t *testing.T) {
	path := t.TempDir() + "/t.db"
	db, err := Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO relays (id, name, slug, token_hash, created_at) VALUES ('r1', 'pi', 'pi', 'h1', CURRENT_TIMESTAMP)`)
	sqlDB, _ := db.DB()
	if err := migrations.DownTo(sqlDB, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := migrations.Run(sqlDB); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := BackfillSlugs(db); err != nil {
		t.Fatal(err)
	}
}
