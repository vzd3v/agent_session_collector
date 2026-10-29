package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/schema"
)

// All catalog writers use one OS lock, including migrations. Receivers are
// independent; deletion still verifies the selected physical generation.
func lockHub(ctx context.Context, catalog string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(catalog), 0o750); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(catalog+".lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open hub lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), catalog+".lock")
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock hub: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func Migrate(ctx context.Context, cfg config.HubAppConfig) (int, error) {
	unlock, err := lockHub(ctx, cfg.Paths.Catalog)
	if err != nil {
		return 0, err
	}
	defer unlock()
	db, err := schema.Open(cfg.Paths.Catalog)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var before int
	// An uninitialized catalog has no migration table.
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='schema_migrations'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists > 0 {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE target='hub'`).Scan(&before); err != nil {
			return 0, err
		}
	}
	if err := schema.Apply(ctx, db, schema.Hub); err != nil {
		return 0, err
	}
	var after int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE target='hub'`).Scan(&after); err != nil {
		return 0, err
	}
	return after - before, nil
}

func requireRetentionSchema(ctx context.Context, db *sql.DB) error {
	var found int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE target='hub' AND version='005_retention.sql'`).Scan(&found); err != nil || found != 1 {
		return fmt.Errorf("compact -dry-run requires current schema; run asc-hub migrate first")
	}
	return nil
}
