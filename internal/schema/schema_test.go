package schema

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySchemasOnEmptyDB(t *testing.T) {
	for _, target := range []Target{Sender, Hub} {
		t.Run(string(target), func(t *testing.T) {
			db := openMemoryDB(t)
			if err := Apply(context.Background(), db, target); err != nil {
				t.Fatalf("apply %s schema: %v", target, err)
			}
			assertForeignKeysEnabled(t, db)
			assertTableExists(t, db, "schema_migrations")
			switch target {
			case Sender:
				assertTableExists(t, db, "synced_files")
			case Hub:
				assertTableExists(t, db, "sessions")
				assertTableExists(t, db, "session_archives")
			}
		})
	}
}

func TestHubSchemaEnforcesArchiveCascade(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Apply(context.Background(), db, Hub); err != nil {
		t.Fatalf("apply hub schema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO sessions (
  session_key, agent, sender, user, session_id, mirror_path,
  latest_size_bytes, latest_mtime_unix_ns, archive_status, first_seen_at, last_indexed_at
) VALUES (
  'codex:sender-alpha:operator:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
  'codex', 'sender-alpha', 'operator', 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
  'sender-alpha/operator/codex/sessions/rollout.jsonl', 1, 1, 'hot', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'
);
INSERT INTO session_archives (
  session_key, version, path, raw_size_bytes, raw_mtime_unix_ns,
  raw_sha256, archive_sha256, created_at
) VALUES (
  'codex:sender-alpha:operator:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
  0, 'codex/sender-alpha/operator/2026/01/01/session.jsonl.zst',
  1, 1, 'raw', 'archive', '2026-01-01T00:00:00Z'
);
`); err != nil {
		t.Fatalf("insert archive fixture: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE session_key = ?`, "codex:sender-alpha:operator:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var archives int
	if err := db.QueryRow(`SELECT count(*) FROM session_archives`).Scan(&archives); err != nil {
		t.Fatalf("count archives: %v", err)
	}
	if archives != 0 {
		t.Fatalf("archive rows after session delete = %d, want 0", archives)
	}
}

func TestOpenEnablesForeignKeysAcrossPooledConnections(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := range 3 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			assertForeignKeysEnabled(t, db)
		})
	}
}

func TestOpenConfiguresSQLiteConcurrencyPragmas(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("open schema db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var busyTimeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout pragma: %v", err)
	}
	if busyTimeout != 10000 {
		t.Fatalf("busy_timeout pragma = %d, want 10000", busyTimeout)
	}

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode pragma: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode pragma = %q, want wal", journalMode)
	}
}

func TestApplySchemasOnExistingDBFixtures(t *testing.T) {
	tests := []struct {
		name    string
		target  Target
		fixture string
	}{
		{name: "sender", target: Sender, fixture: "sender_existing_v1.sql"},
		{name: "hub", target: Hub, fixture: "hub_existing_v1.sql"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMemoryDB(t)
			sqlBody, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if _, err := db.Exec(string(sqlBody)); err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			if tc.target == Hub {
				if _, err := db.Exec(`INSERT INTO sessions
(session_key,agent,sender,user,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,archive_status,first_seen_at,last_seen_at)
VALUES ('fixture','codex','sender-alpha','operator','fixture','fixture.jsonl',1,1,'hot','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := Apply(context.Background(), db, tc.target); err != nil {
				t.Fatalf("apply existing schema: %v", err)
			}
			wantMigrations := 1
			if tc.target == Hub {
				wantMigrations = 2
				var changedAt, indexedAt string
				if err := db.QueryRow(`SELECT last_changed_at,last_indexed_at FROM sessions LIMIT 1`).Scan(&changedAt, &indexedAt); err != nil {
					t.Fatal(err)
				}
				if changedAt != "" || indexedAt == "" {
					t.Fatalf("migration fabricated freshness: changed=%q indexed=%q", changedAt, indexedAt)
				}
			}
			assertMigrationCount(t, db, tc.target, wantMigrations)
		})
	}
}

func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertTableExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err != nil {
		t.Fatalf("table %s does not exist: %v", name, err)
	}
}

func assertForeignKeysEnabled(t *testing.T, db *sql.DB) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys pragma = %d, want 1", enabled)
	}
}

func assertMigrationCount(t *testing.T, db *sql.DB, target Target, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE target = ?`, target).Scan(&got); err != nil {
		t.Fatalf("query migration count: %v", err)
	}
	if got != want {
		t.Fatalf("migration count = %d, want %d", got, want)
	}
}
