package schema

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
)

//go:embed migrations/*/*.sql
var migrationsFS embed.FS

type Target string

const (
	Sender Target = "sender"
	Hub    Target = "hub"
)

type Migration struct {
	Version string
	SQL     string
}

func Open(path string) (*sql.DB, error) {
	dsn := sqliteDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

func Apply(ctx context.Context, db *sql.DB, target Target) error {
	if target != Sender && target != Hub {
		return fmt.Errorf("unknown schema target %q", target)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
target TEXT NOT NULL,
version TEXT NOT NULL,
applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
PRIMARY KEY (target, version)
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := List(target)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		applied, err := migrationApplied(ctx, db, target, migration.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, target) VALUES (?, ?)`, migration.Version, target); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", migration.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.Version, err)
		}
	}
	return nil
}

func sqliteDSN(path string) string {
	values := url.Values{}
	values.Set("_pragma", "foreign_keys(1)")
	if path == ":memory:" {
		return path + "?" + values.Encode()
	}
	return "file:" + url.PathEscape(path) + "?" + values.Encode()
}

func List(target Target) ([]Migration, error) {
	dir := path.Join("migrations", string(target))
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations for %s: %w", target, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	out := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version := entry.Name()
		body, err := migrationsFS.ReadFile(path.Join(dir, version))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", version, err)
		}
		out = append(out, Migration{Version: version, SQL: string(body)})
	}
	return out, nil
}

func migrationApplied(ctx context.Context, db *sql.DB, target Target, version string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = ? AND target = ?`, version, target).Scan(&found)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("query migration %s: %w", version, err)
}
