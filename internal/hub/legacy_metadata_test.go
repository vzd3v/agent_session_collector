package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/schema"
)

func TestLegacyCompactionDefersMetadataUntilIndexed(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/"+id+".jsonl", fmt.Sprintf("{\"sessionId\":%q}\n", id))
	rawHash, rawSize, err := physicalFile(ctx, mirror)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Paths.Archive, 0750); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cfg.Paths.Archive, "legacy.jsonl.zst")
	compressed, err := exec.Command("zstd", "-q", "-c", mirror).Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, compressed, 0600); err != nil {
		t.Fatal(err)
	}
	archiveHash, _, err := physicalFile(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mirror); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Paths.Catalog), 0750); err != nil {
		t.Fatal(err)
	}
	db, err := schema.Open(cfg.Paths.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE schema_migrations(target TEXT NOT NULL,version TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT '',PRIMARY KEY(target,version))"); err != nil {
		t.Fatal(err)
	}
	migrations, err := schema.List(schema.Hub)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:3] {
		if _, err := db.Exec(m.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations(target,version) VALUES('hub',?)", m.Version); err != nil {
			t.Fatal(err)
		}
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.Exec("INSERT INTO sessions(session_key,agent,sender,user,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,latest_raw_sha256,archive_status,latest_archive_path,latest_archive_sha256,first_seen_at,last_indexed_at) VALUES('legacy','claude','sender-alpha','operator',?,?,?,?,?,'archived',?,?,?,?)", id, mirror, rawSize, now.Add(-48*time.Hour).UnixNano(), rawHash, archive, archiveHash, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_archives(session_key,version,path,raw_size_bytes,raw_mtime_unix_ns,raw_sha256,archive_sha256,created_at) VALUES('legacy',0,?,?,?,?,?,?)", archive, rawSize, now.Add(-48*time.Hour).UnixNano(), rawHash, archiveHash, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive + ".metadata.json"); !os.IsNotExist(err) {
		t.Fatalf("unindexed legacy snapshot published: %v", err)
	}
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	var dbSource, dbArtifact, dbNative, dbRelative string
	if err := db.QueryRow("SELECT source_id,artifact_id,native_session_id,relative_path FROM sessions WHERE session_key='legacy'").Scan(&dbSource, &dbArtifact, &dbNative, &dbRelative); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(archive + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var side struct {
		Source   string         `json:"source_id"`
		Artifact string         `json:"artifact_id"`
		Relative string         `json:"relative_path"`
		Native   map[string]any `json:"native_identity"`
	}
	if err := json.Unmarshal(after, &side); err != nil {
		t.Fatal(err)
	}
	if side.Source == "" || side.Source != dbSource || side.Artifact != dbArtifact || side.Relative != dbRelative || dbNative != id {
		t.Fatal("legacy snapshot does not match indexed identities")
	}
	if got, _, err := physicalFile(ctx, archive); err != nil || got != archiveHash {
		t.Fatalf("legacy archive changed: %v", err)
	}
}
