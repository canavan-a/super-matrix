package database

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// An IP group in use can't be deleted, whatever the API's own check saw:
// the foreign key refuses it, and the API tells that apart by
// gorm.ErrForeignKeyViolated. Names are unique regardless of case, and
// deleting a deployment or bridge takes its group rows with it.
func TestIPGroupConstraints(t *testing.T) {
	db, err := Connect(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Create(&Relay{ID: "r1", Name: "r1", TokenHash: "r1", CreatedAt: now}).Error)
	must(db.Create(&Domain{ID: "d1", Name: "test", Wildcard: true, CreatedAt: now}).Error)
	dom, host := "d1", "app.test"
	must(db.Create(&Deployment{ID: "dep", RelayID: "r1", DomainID: &dom, Subdomain: "app", Host: &host,
		Protocol: ProtocolHTTP, TargetHost: "127.0.0.1", TargetPort: 80, AuthMode: AuthNone, CreatedAt: now, UpdatedAt: now}).Error)
	must(db.Create(&Bridge{ID: "br", Name: "br", Protocol: ProtocolTCP, ListenHost: "127.0.0.1", ListenPort: 3478,
		ToRelayID: "r1", TargetHost: "127.0.0.1", TargetPort: 3478, UDPIdleS: 300, CreatedAt: now, UpdatedAt: now}).Error)
	must(db.Create(&IPGroup{ID: "g1", Name: "Office", Entries: "10.0.0.0/8", CreatedAt: now, UpdatedAt: now}).Error)

	if err := db.Create(&IPGroup{ID: "g2", Name: "office", Entries: "10.0.0.1/32", CreatedAt: now, UpdatedAt: now}).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Errorf("same name, other case = %v, want gorm.ErrDuplicatedKey", err)
	}
	if err := db.Create(&DeploymentIPGroup{DeploymentID: "dep", IPGroupID: "nope"}).Error; !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Errorf("unknown group = %v, want gorm.ErrForeignKeyViolated", err)
	}

	for _, row := range []any{&DeploymentIPGroup{DeploymentID: "dep", IPGroupID: "g1"}, &BridgeIPGroup{BridgeID: "br", IPGroupID: "g1"}} {
		must(db.Create(row).Error)
	}
	if err := db.Delete(&IPGroup{ID: "g1"}).Error; !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Errorf("delete in use = %v, want gorm.ErrForeignKeyViolated", err)
	}

	must(db.Delete(&Deployment{ID: "dep"}).Error)
	must(db.Delete(&Bridge{ID: "br"}).Error)
	var n int64
	db.Model(&DeploymentIPGroup{}).Count(&n)
	var nb int64
	db.Model(&BridgeIPGroup{}).Count(&nb)
	if n != 0 || nb != 0 {
		t.Fatalf("rows left after deleting their owners: %d deployment, %d bridge", n, nb)
	}
	must(db.Delete(&IPGroup{ID: "g1"}).Error)
}
