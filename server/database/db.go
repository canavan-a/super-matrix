package database

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"main/database/migrations"
)

// Connect opens (creating if needed) the SQLite database at path and applies
// pending migrations.
func Connect(path string) (*gorm.DB, error) {
	// sqlite reports a missing parent directory as a misleading "out of
	// memory (14)", so make sure it exists first.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}

	// WAL + a busy timeout let concurrent requests queue briefly on a write
	// instead of failing with SQLITE_BUSY. foreign_keys makes ON DELETE
	// CASCADE work, so deleting a deployment takes its tokens along.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			// Never log query parameters: they include password and token hashes.
			ParameterizedQueries: true,
		},
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, err
	}

	// SQLite allows one writer at a time regardless of WAL; one connection
	// serializes writes through Go's own queueing instead.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)

	if err := migrations.Run(sqlDB); err != nil {
		return nil, err
	}
	return db, nil
}
