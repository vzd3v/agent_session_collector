package sender

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/schema"
)

type stateStore struct {
	db *sql.DB
}

type syncedFile struct {
	SourcePath  string
	SizeBytes   int64
	MtimeUnixNS int64
}

func openState(ctx context.Context, path string) (*stateStore, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}
	db, err := schema.Open(path)
	if err != nil {
		return nil, err
	}
	if err := schema.Apply(ctx, db, schema.Sender); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &stateStore{db: db}, nil
}

func openStateIfExists(ctx context.Context, path string) (*stateStore, error) {
	if path == ":memory:" {
		return nil, nil
	}
	if path != ":memory:" {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("stat sender state: %w", err)
		}
	}
	db, err := schema.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	exists, err := tableExists(ctx, db, "synced_files")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !exists {
		_ = db.Close()
		return nil, nil
	}
	return &stateStore{db: db}, nil
}

func (s *stateStore) Close() error {
	return s.db.Close()
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("query sqlite table %s: %w", name, err)
}

func (s *stateStore) get(ctx context.Context, sourceKey string) (syncedFile, bool, error) {
	var row syncedFile
	err := s.db.QueryRowContext(ctx, `
SELECT source_path, size_bytes, mtime_unix_ns
FROM synced_files
WHERE source_key = ?`, sourceKey).Scan(&row.SourcePath, &row.SizeBytes, &row.MtimeUnixNS)
	if err == nil {
		return row, true, nil
	}
	if err == sql.ErrNoRows {
		return syncedFile{}, false, nil
	}
	return syncedFile{}, false, fmt.Errorf("query sender state: %w", err)
}

func (s *stateStore) markSynced(ctx context.Context, files []File, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sender state update: %w", err)
	}
	for _, file := range files {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO synced_files (
  source_key, agent, user, source_path, session_id,
  size_bytes, mtime_unix_ns, last_synced_at, last_error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(source_key) DO UPDATE SET
  agent = excluded.agent,
  user = excluded.user,
  source_path = excluded.source_path,
  session_id = excluded.session_id,
  size_bytes = excluded.size_bytes,
  mtime_unix_ns = excluded.mtime_unix_ns,
  last_synced_at = excluded.last_synced_at,
  last_error = ''`,
			file.SourceKey,
			file.Agent,
			file.User,
			file.SourcePath,
			file.SessionID,
			file.SizeBytes,
			file.MtimeUnixNS,
			now.UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert synced file %s: %w", file.DisplayPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sender state update: %w", err)
	}
	return nil
}

func planUploads(ctx context.Context, store *stateStore, files []File) (Plan, error) {
	plan := Plan{}
	for _, file := range files {
		if store == nil {
			plan.Planned = append(plan.Planned, file)
			continue
		}
		state, found, err := store.get(ctx, file.SourceKey)
		if err != nil {
			return Plan{}, err
		}
		if found && state.SourcePath == file.SourcePath && state.SizeBytes == file.SizeBytes && state.MtimeUnixNS == file.MtimeUnixNS {
			plan.Skipped = append(plan.Skipped, file)
			continue
		}
		plan.Planned = append(plan.Planned, file)
	}
	return plan, nil
}
