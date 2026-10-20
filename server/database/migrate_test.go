package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"main/database/migrations"
)

// Deployments from before domains keep their tokens through the table
// rebuild, and get the base domain and their host from Seed.
func TestDomainsMigration(t *testing.T) {
	path := t.TempDir() + "/old.db"
	old, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := old.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := migrations.UpTo(sqlDB, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO relays (id, name, token_hash, created_at) VALUES ('r1', 'pi', 'h', CURRENT_TIMESTAMP)`,
		`INSERT INTO deployments (id, relay_id, subdomain, target_host, target_port, created_at, updated_at) VALUES ('d1', 'r1', 'grafana', '127.0.0.1', 3000, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO deployment_tokens (id, deployment_id, name, token_hash, created_at) VALUES ('t1', 'd1', 'ci', 'th', CURRENT_TIMESTAMP)`,
	} {
		if err := old.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB.Close()

	db, err := Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Seed(db, "example.com"); err != nil {
		t.Fatal(err)
	}
	d, err := First[Deployment](db.Where("id = ?", "d1"))
	if err != nil || d == nil || d.Host == nil || *d.Host != "grafana.example.com" || d.DomainID == nil {
		t.Fatalf("deployment after migration: %+v %v", d, err)
	}
	var toks int64
	db.Model(&DeploymentToken{}).Where("deployment_id = ?", "d1").Count(&toks)
	if toks != 1 {
		t.Fatalf("tokens lost in the rebuild: %d", toks)
	}
	var doms []Domain
	db.Find(&doms)
	if len(doms) != 1 || doms[0].Label() != "*.example.com" {
		t.Fatalf("domains: %+v", doms)
	}
	// Seeding again changes nothing; deleting a deployment still cascades.
	if err := Seed(db, "example.com"); err != nil {
		t.Fatal(err)
	}
	db.Model(&Domain{}).Count(&toks)
	if toks != 1 {
		t.Fatalf("seeded twice: %d domains", toks)
	}
	db.Delete(&Deployment{}, "id = ?", "d1")
	db.Model(&DeploymentToken{}).Count(&toks)
	if toks != 0 {
		t.Fatal("foreign keys are off after the migration")
	}
}
