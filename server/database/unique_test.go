package database

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// The API answers 409 for a write that loses a race past its own
// conflict check by looking for gorm.ErrDuplicatedKey, so a clash on
// either bridge index must come back as that.
func TestBridgeIndexesReportDuplicatedKey(t *testing.T) {
	db, err := Connect(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"r1", "r2"} {
		if err := db.Create(&Relay{ID: id, Name: id, TokenHash: id, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	from := "r1"
	bridge := func(id string, fromRelay *string) *Bridge {
		return &Bridge{ID: id, Name: id, Protocol: ProtocolTCP, Enabled: true, FromRelayID: fromRelay,
			ListenHost: "127.0.0.1", ListenPort: 5432, ToRelayID: "r2", TargetHost: "db", TargetPort: 5432,
			UDPIdleS: 300, CreatedAt: now, UpdatedAt: now}
	}

	for name, pair := range map[string][2]*Bridge{
		"bridges_listen": {bridge("a", &from), bridge("b", &from)},
		"bridges_public": {bridge("c", nil), bridge("d", nil)},
	} {
		if err := db.Create(pair[0]).Error; err != nil {
			t.Fatalf("%s: first insert: %v", name, err)
		}
		if err := db.Create(pair[1]).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Errorf("%s: second insert = %v, want gorm.ErrDuplicatedKey", name, err)
		}
	}
}
