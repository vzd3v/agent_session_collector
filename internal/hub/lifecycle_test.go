package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/schema"
)

func TestCompactPreservesPlainWithoutValidArchive(t *testing.T) {
	requireZstd(t)
	for _, damage := range []string{"missing", "corrupt", "metadata"} {
		t.Run(damage, func(t *testing.T) {
			cfg := testHubConfig(t.TempDir())
			cfg.Retention.DeleteMirrorAfterArchive = true
			now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
			mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/session.jsonl", "only good copy\n")
			setFileMtime(t, mirror, now.Add(-48*time.Hour))
			indexFixture(t, cfg, now)
			if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
				t.Fatal(err)
			}
			db := openCatalog(t, cfg.Paths.Catalog)
			var archive string
			if err := db.QueryRow(`SELECT latest_archive_path FROM sessions`).Scan(&archive); err != nil {
				t.Fatal(err)
			}
			if damage == "missing" {
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			} else {
				target := archive
				if damage == "metadata" {
					target += ".metadata.json"
				}
				if err := os.WriteFile(target, []byte("corrupted"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(49 * time.Hour) }}); err == nil {
				t.Fatal("expected verification error")
			}
			if got, err := os.ReadFile(mirror); err != nil || string(got) != "only good copy\n" {
				t.Fatalf("plain copy lost: %q %v", got, err)
			}
		})
	}
}

func TestLifecyclePreviewExpiryReplayAndNewContent(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	cfg.Retention.ArchiveMaxAge = config.Duration{Duration: 24 * time.Hour}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	rel := "operator/claude/projects/sample/session.jsonl"
	mirror := writeMirrorFile(t, cfg, rel, "first generation\n")
	setFileMtime(t, mirror, now.Add(-time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, cfg.Paths.Root)
	later := now.Add(25 * time.Hour)
	preview, err := Compact(ctx, cfg, CompactOptions{DryRun: true, Now: func() time.Time { return later }})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Plan.Actions) != 1 || preview.Plan.Actions[0].Reason != "archive_max_age" {
		t.Fatalf("preview=%+v", preview.Plan)
	}
	if after := treeDigest(t, cfg.Paths.Root); before != after {
		t.Fatal("dry run changed storage")
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return later }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired rows=%d err=%v", count, err)
	}
	writeMirrorFile(t, cfg, rel, "first generation\n")
	result, err := Index(ctx, cfg, Options{Now: func() time.Time { return later }})
	if err != nil || result.Upserted != 0 || result.Skipped != 1 {
		t.Fatalf("replay=%+v %v", result, err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return later }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatalf("expired replay remains: %v", err)
	}
	writeMirrorFile(t, cfg, rel, "second generation\n")
	setFileMtime(t, mirror, later.Add(-time.Hour))
	result, err = Index(ctx, cfg, Options{Now: func() time.Time { return later }})
	if err != nil || result.Upserted != 1 {
		t.Fatalf("new content=%+v %v", result, err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return later }}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new generation missing: %d %v", count, err)
	}
}

func TestArchiveReplacementAndInactiveVersionMaintenance(t *testing.T) {
	requireZstd(t)
	for _, policy := range []string{"replace", "versioned"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			cfg := testHubConfig(t.TempDir())
			cfg.Archive.Policy = policy
			cfg.Archive.VersionRetention = 1
			cfg.Archive.VersionRetentionDays = 1
			cfg.Archive.QuietAfter = config.Duration{}
			now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
			rel := "operator/claude/projects/sample/session.jsonl"
			for i := range 2 {
				mirror := writeMirrorFile(t, cfg, rel, fmt.Sprintf("generation %d\n", i))
				at := now.Add(time.Duration(i) * time.Hour)
				setFileMtime(t, mirror, at.Add(-time.Hour))
				indexFixture(t, cfg, at)
				if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return at }}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(40 * 24 * time.Hour) }}); err != nil {
				t.Fatal(err)
			}
			db := openCatalog(t, cfg.Paths.Catalog)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM session_archives`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("archive rows=%d %v", count, err)
			}
			var physical int
			if err := filepath.WalkDir(cfg.Paths.Archive, func(name string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(name, ".zst") {
					physical++
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if physical != 1 {
				t.Fatalf("physical archives=%d", physical)
			}
		})
	}
}

func TestCodexParentPagesExpireAsGroupAndKeepForkIndependent(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	cfg.Retention.ArchiveMaxAge = config.Duration{Duration: 24 * time.Hour}
	ids := []string{"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee", "cccccccc-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	write := func(id, suffix, extra string) {
		mirror := writeMirrorFile(t, cfg, "operator/codex/.codex/sessions/page"+suffix+"-"+id+".jsonl", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q%s}}`+"\n", id, extra))
		setFileMtime(t, mirror, now.Add(-time.Hour))
	}
	write(ids[0], "1", "")
	write(ids[1], "1", `,"source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+ids[0]+`"}}}`)
	write(ids[1], "2", `,"parent_thread_id":"`+ids[0]+`"`)
	index := writeMirrorFile(t, cfg, "operator/codex/.codex/session_index.jsonl", "{\"thread_name\":\"sample\"}\n")
	setFileMtime(t, index, now.Add(-time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	// A new fork is separate even though its ancestry names the old root.
	later := now.Add(25 * time.Hour)
	write(ids[2], "1", `,"forked_from_id":"`+ids[0]+`"`)
	indexFixture(t, cfg, later)
	preview, err := Compact(ctx, cfg, CompactOptions{DryRun: true, Now: func() time.Time { return later }})
	if err != nil {
		t.Fatal(err)
	}
	var expired int
	for _, action := range preview.Plan.Actions {
		if action.Reason == "archive_max_age" {
			expired += len(action.Sessions)
		}
	}
	if expired != 3 {
		t.Fatalf("expected root+2 child pages; plan=%+v", preview.Plan)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return later }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("fork and source index should remain: %d %v", count, err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return later.Add(25 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("last source group should retire its one index: %d %v", count, err)
	}
}

func TestArchiveByteTargetExpiresOldestEligibleGroup(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	for i, name := range []string{"old", "new"} {
		mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/"+name+".jsonl", name+"\n")
		at := now.Add(time.Duration(i) * time.Hour)
		setFileMtime(t, mirror, at.Add(-time.Hour))
		indexFixture(t, cfg, at)
		if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return at }}); err != nil {
			t.Fatal(err)
		}
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var keepPath string
	if err := db.QueryRow(`SELECT latest_archive_path FROM sessions WHERE session_id='new'`).Scan(&keepPath); err != nil {
		t.Fatal(err)
	}
	var target int64
	for _, name := range []string{keepPath, keepPath + ".metadata.json"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		target += info.Size()
	}
	cfg.Retention.ArchiveMaxBytes = config.ByteSize{Bytes: target}
	preview, err := Compact(ctx, cfg, CompactOptions{DryRun: true, Now: func() time.Time { return now.Add(25 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Plan.Actions) != 1 || preview.Plan.Actions[0].Reason != "archive_bytes" {
		t.Fatalf("plan=%+v", preview.Plan)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(25 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	var retained string
	if err := db.QueryRow(`SELECT session_id FROM sessions`).Scan(&retained); err != nil || retained != "new" {
		t.Fatalf("retained=%q err=%v", retained, err)
	}
}

func TestClaudeToolAssetsFollowCorroboratedRoot(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	cfg.Retention.ArchiveMaxAge = config.Duration{Duration: 24 * time.Hour}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	for rel, body := range map[string]string{"projects/sample/" + id + ".jsonl": `{"sessionId":"` + id + `"}` + "\n", "projects/sample/" + id + "/subagents/agent-child.meta.json": `{"agentType":"reviewer"}`, "projects/sample/" + id + "/tool-results/output.txt": "captured result", "projects/sample/unknown/tool-results/output.txt": "unknown owner"} {
		mirror := writeMirrorFile(t, cfg, "operator/claude/"+rel, body)
		setFileMtime(t, mirror, now.Add(-time.Hour))
	}
	indexFixture(t, cfg, now)
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	preview, err := Compact(ctx, cfg, CompactOptions{DryRun: true, Now: func() time.Time { return now.Add(25 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Plan.Actions) != 1 || len(preview.Plan.Actions[0].Sessions) != 3 {
		t.Fatalf("root/meta/tool should expire together: %+v", preview.Plan)
	}
	if len(preview.Plan.Protected) != 1 || preview.Plan.Protected[0].Reason != "unknown_companion_owner" {
		t.Fatalf("unknown tool owner unprotected: %+v", preview.Plan)
	}
}

func TestPressureProtectsActiveAndUnknownClock(t *testing.T) {
	requireZstd(t)
	cfg := testHubConfig(t.TempDir())
	cfg.Retention.ArchiveMaxBytes = config.ByteSize{Bytes: 1}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/session.jsonl", "protected\n")
	setFileMtime(t, mirror, now.Add(-48*time.Hour))
	indexFixture(t, cfg, now)
	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }})
	if err == nil || result.Plan == nil || !result.Plan.PressureUnsatisfied {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	if _, err := db.Exec(`UPDATE sessions SET last_changed_at='bad',first_seen_at='bad'`); err != nil {
		t.Fatal(err)
	}
	result, err = Compact(context.Background(), cfg, CompactOptions{DryRun: true, Now: func() time.Time { return now.Add(400 * 24 * time.Hour) }})
	if err == nil || len(result.Plan.Actions) != 0 {
		t.Fatalf("unknown clock was not protected: %+v %v", result, err)
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveFreeFloorRejectsBeforeCompressedWrite(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Retention.MinFreeBytes = config.ByteSize{Bytes: math.MaxInt64}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/session.jsonl", "keep this source\n")
	setFileMtime(t, mirror, now.Add(-48*time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err == nil || !strings.Contains(err.Error(), "archive admission") {
		t.Fatalf("admission err=%v", err)
	}
	if b, err := os.ReadFile(mirror); err != nil || string(b) != "keep this source\n" {
		t.Fatalf("source changed %q %v", b, err)
	}
	if err := filepath.WalkDir(cfg.Paths.Archive, func(name string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("unexpected compressed write %s", name)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionRecoveryPreservesNewReceiverGeneration(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	if _, err := Migrate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	for _, stage := range []string{"intent", "renamed", "unlinked", "new_generation"} {
		t.Run(stage, func(t *testing.T) {
			name := writeMirrorFile(t, cfg, "operator/claude/projects/sample/"+stage+".jsonl", "old bytes\n")
			hash, size, err := physicalFile(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			d, err := newDeletion(cfg, "mirror", name, hash, "expired_replay", size, now)
			if err != nil {
				t.Fatal(err)
			}
			db := openCatalog(t, cfg.Paths.Catalog)
			if err := queueDeletion(ctx, db, d, now); err != nil {
				t.Fatal(err)
			}
			if stage == "renamed" || stage == "unlinked" {
				if err := os.MkdirAll(filepath.Join(cfg.Paths.Mirror, ".asc-trash"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(name, filepath.Join(cfg.Paths.Mirror, d.Quarantine)); err != nil {
					t.Fatal(err)
				}
				if stage == "unlinked" {
					if err := os.Remove(filepath.Join(cfg.Paths.Mirror, d.Quarantine)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if stage == "new_generation" {
				if err := os.WriteFile(name, []byte("new receiver bytes\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
				t.Fatal(err)
			}
			if stage == "new_generation" {
				b, err := os.ReadFile(name)
				if err != nil || string(b) != "new receiver bytes\n" {
					t.Fatalf("new generation lost %q %v", b, err)
				}
			} else if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Fatalf("old file remains: %v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pending_deletions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("pending=%d %v", count, err)
			}
		})
	}
}

func TestOrphanGraceStartsAtDiscovery(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Retention.OrphanGrace = config.Duration{Duration: 24 * time.Hour}
	if _, err := Migrate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Paths.Archive, 0o750); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(cfg.Paths.Archive, "old.aaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbb.jsonl.zst")
	if err := os.WriteFile(name, []byte("unreferenced old object"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, name, now.Add(-400*24*time.Hour))
	for _, at := range []time.Time{now, now.Add(23 * time.Hour)} {
		if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return at }}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("orphan grace shortened by mtime: %v", err)
		}
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(25 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("expired orphan remains: %v", err)
	}
}

func TestMigrateUpgradeAndReadOnlyPrerequisite(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(cfg.Paths.Catalog), 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := schema.Open(cfg.Paths.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(target TEXT,version TEXT,PRIMARY KEY(target,version))`); err != nil {
		t.Fatal(err)
	}
	migrations, err := schema.List(schema.Hub)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version == "005_retention.sql" {
			continue
		}
		if _, err := db.Exec(migration.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES('hub',?)`, migration.Version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_key,agent,sender,user,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,archive_status,first_seen_at,last_indexed_at) VALUES('legacy','claude','sender-alpha','operator','session','/synthetic/missing',1,1,'hot','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("prerequisite error=%v", err)
	}
	for i, want := range []int{1, 0} {
		got, err := Migrate(ctx, cfg)
		if err != nil || got != want {
			t.Fatalf("migration pass%d applied=%d want=%d err=%v", i, got, want, err)
		}
	}
	db = openCatalog(t, cfg.Paths.Catalog)
	var key string
	if err := db.QueryRow(`SELECT session_key FROM sessions`).Scan(&key); err != nil || key != "legacy" {
		t.Fatalf("legacy identity lost %q %v", key, err)
	}
}

func TestMigrateWaitUsesSharedCancellableWriterLock(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	if _, err := Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(cfg.Paths.Catalog+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Migrate(ctx, cfg); err != context.Canceled {
		t.Fatalf("expected cancellation while lock held: %v", err)
	}
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	if err := filepath.WalkDir(root, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// SQLite mode=ro still participates in WAL reader coordination. SHM is
		// transient coordination state, not persisted catalog/artifact content.
		if strings.HasSuffix(name, "-shm") {
			return nil
		}
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, "-wal") && len(b) == 0 {
			return nil
		}
		_, _ = fmt.Fprintf(h, "%s\x00%x\n", name, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
