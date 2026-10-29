package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/diskspace"
)

func expiryGroupFixture(t *testing.T) (config.HubAppConfig, *sql.DB, string, string, time.Time, RetentionPlan) {
	t.Helper()
	requireZstd(t)
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	cfg.Retention.ArchiveMaxAge = config.Duration{Duration: time.Hour}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	root := writeMirrorFile(t, cfg, "operator/claude/projects/sample/"+id+".jsonl", fmt.Sprintf("{\"sessionId\":%q}\n", id))
	child := writeMirrorFile(t, cfg, "operator/claude/projects/sample/"+id+"/subagents/agent-worker.jsonl", fmt.Sprintf("{\"sessionId\":%q}\n", id))
	setFileMtime(t, root, now.Add(-time.Hour))
	setFileMtime(t, child, now.Add(-time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	later := now.Add(2 * time.Hour)
	plan, err := buildRetentionPlan(context.Background(), db, cfg, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.expire) != 1 || len(plan.expire[0]) != 2 {
		t.Fatalf("expected one linked group: %+v", plan)
	}
	return cfg, db, root, child, later, plan
}

func TestWholeGroupExpiryRejectsStaleMemberAndUnknownNewChild(t *testing.T) {
	for _, arrival := range []string{"changed_member", "new_child"} {
		t.Run(arrival, func(t *testing.T) {
			cfg, db, root, child, now, plan := expiryGroupFixture(t)
			name := child
			if arrival == "new_child" {
				name = filepath.Join(filepath.Dir(child), "agent-new.jsonl")
			}
			if err := os.WriteFile(name, []byte("new receiver generation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := applyRetentionPlan(context.Background(), db, cfg, plan, now); err != nil {
				t.Fatal(err)
			}
			if _, err := drainDeletions(context.Background(), db, cfg, now); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{root, child} {
				if _, err := os.Stat(name); err != nil {
					t.Fatalf("active group lost member: %v", err)
				}
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("catalog group lost: %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM expiry_tombstones`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("aborted group tombstoned: %d %v", count, err)
			}
		})
	}
}

func TestWholeGroupReservationCrashAndLaterArrivals(t *testing.T) {
	for _, stage := range []string{"uncommitted", "uncommitted_new_arrival", "committed_new_arrival"} {
		t.Run(stage, func(t *testing.T) {
			cfg, db, root, child, now, plan := expiryGroupFixture(t)
			ctx := context.Background()
			id, ready, err := reserveExpiryGroup(ctx, db, cfg, plan.expire[0], now)
			if err != nil || !ready {
				t.Fatalf("reserve ready=%t err=%v", ready, err)
			}
			if stage == "committed_new_arrival" {
				if err := commitExpiryGroup(ctx, db, cfg, id, plan.expire[0], now); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(stage, "new_arrival") {
				if err := os.WriteFile(child, []byte("later generation\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Retention.ArchiveMaxAge = config.Duration{} // Observe recovery, without a new expiry decision.
			_, err = Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }})
			if stage == "uncommitted_new_arrival" {
				if err == nil {
					t.Fatal("expected preserved quarantine conflict")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stage, "new_arrival") {
				b, err := os.ReadFile(child)
				if err != nil || string(b) != "later generation\n" {
					t.Fatalf("later generation lost %q %v", b, err)
				}
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if stage == "committed_new_arrival" {
				if count != 0 {
					t.Fatalf("committed group not expired: %d", count)
				}
			} else {
				if count != 2 {
					t.Fatalf("uncommitted catalog lost: %d", count)
				}
				if _, err := os.Stat(root); err != nil {
					t.Fatalf("uncommitted root not restored: %v", err)
				}
			}
		})
	}
}

func TestRecoveredPlainDeletionRechecksBackingArchive(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "metadata"} {
		for _, stage := range []string{"queued", "quarantined"} {
			t.Run(damage+"/"+stage, func(t *testing.T) {
				cfg, db, root, _, now, _ := expiryGroupFixture(t)
				cfg.Retention.ArchiveMaxAge = config.Duration{}
				var archive string
				if err := db.QueryRow(`SELECT latest_archive_path FROM sessions WHERE mirror_path=?`, root).Scan(&archive); err != nil {
					t.Fatal(err)
				}
				hash, size, err := physicalFile(context.Background(), root)
				if err != nil {
					t.Fatal(err)
				}
				d, err := newDeletion(cfg, "mirror", root, hash, "archived_plain", size, now)
				if err != nil {
					t.Fatal(err)
				}
				if err := queueDeletion(context.Background(), db, d, now); err != nil {
					t.Fatal(err)
				}
				if stage == "quarantined" {
					if err := ensureQuarantine(cfg.Paths.Mirror); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(root, filepath.Join(cfg.Paths.Mirror, d.Quarantine)); err != nil {
						t.Fatal(err)
					}
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
					if err := os.WriteFile(target, []byte("corrupt"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err == nil {
					t.Fatal("expected backing validation failure")
				}
				if _, err := os.Stat(root); err != nil {
					t.Fatalf("last plain not preserved/restored: %v", err)
				}
			})
		}
	}
}

func TestMirrorOnlyPressureCannotExpireArchiveOnlyGroups(t *testing.T) {
	requireZstd(t)
	mirror, err := os.MkdirTemp("/dev/shm", "asc-retention-test-*")
	if err != nil {
		t.Skip("separate tmpfs unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(mirror) })
	cfg := testHubConfig(t.TempDir())
	same, err := diskspace.SameFilesystem(cfg.Paths.Archive, mirror)
	if err != nil || same {
		t.Skip("separate filesystems unavailable")
	}
	cfg.Paths.Root = "/"
	cfg.Paths.Mirror = mirror
	cfg.Archive.QuietAfter = config.Duration{}
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	cfg.Retention.DeleteMirrorAfterArchive = true
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	name := writeMirrorFile(t, cfg, "operator/claude/projects/sample/session.jsonl", "archived-only\n")
	setFileMtime(t, name, now.Add(-time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	available, err := diskspace.Available(mirror)
	if err != nil {
		t.Fatal(err)
	}
	archiveFree, err := diskspace.Available(cfg.Paths.Archive)
	if err != nil || archiveFree <= available+1 {
		t.Skip("archive filesystem not roomier than mirror")
	}
	cfg.Retention.MinFreeBytes = config.ByteSize{Bytes: available + 1}
	result, err := Compact(context.Background(), cfg, CompactOptions{DryRun: true, Now: func() time.Time { return now.Add(25 * time.Hour) }})
	if err == nil || result.Plan == nil || !result.Plan.PressureUnsatisfied || len(result.Plan.Actions) != 0 {
		t.Fatalf("zero-relief expiry plan=%+v err=%v", result.Plan, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("archive-only group lost: %d %v", count, err)
	}
}

func TestReplacementDoesNotPublishBeforeMetadataIsDurable(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Archive.QuietAfter = config.Duration{}
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	mirror := writeMirrorFile(t, cfg, "operator/claude/projects/sample/session.jsonl", "old content\n")
	setFileMtime(t, mirror, now.Add(-time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var oldPath string
	if err := db.QueryRow(`SELECT path FROM session_archives`).Scan(&oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirror, []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, mirror, now)
	indexFixture(t, cfg, now.Add(time.Hour))
	sessions, err := loadHotSessions(ctx, db)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("hot sessions=%d err=%v", len(sessions), err)
	}
	blocked := archivePath(cfg, sessions[0], 0) + ".metadata.json"
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(time.Hour) }}); err == nil {
		t.Fatal("expected metadata publication failure")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pending_deletions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retirement published before metadata: %d %v", count, err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("previous good archive lost: %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(time.Hour) }}); err != nil {
		t.Fatalf("publication retry failed: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM session_archives`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replacement rows=%d err=%v", count, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("superseded archive remains: %v", err)
	}
}

func TestCompactBackfillsCurrentArchiveOnlyMetadata(t *testing.T) {
	cfg, db, root, _, now, _ := expiryGroupFixture(t)
	cfg.Retention.ArchiveMaxAge = config.Duration{}
	var archive string
	if err := db.QueryRow(`SELECT latest_archive_path FROM sessions WHERE mirror_path=?`, root).Scan(&archive); err != nil {
		t.Fatal(err)
	}
	before, _, err := physicalFile(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET metadata_json='{"team":"synthetic"}',host_name='sender.example.net',env='test',data_class='agent_transcript' WHERE mirror_path=?`, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archive + ".metadata.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Provenance, ArtifactID, Role, Env string
		Metadata                          map[string]string
		Native                            map[string]any
	}
	b, err := os.ReadFile(archive + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["provenance"], &snapshot.Provenance); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["metadata"], &snapshot.Metadata); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["native_identity"], &snapshot.Native); err != nil {
		t.Fatal(err)
	}
	if snapshot.Provenance != "legacy_backfill_current_metadata" || snapshot.Metadata["team"] != "synthetic" || snapshot.Native["root_id"] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("snapshot=%s", b)
	}
	if after, _, err := physicalFile(context.Background(), archive); err != nil || before != after {
		t.Fatalf("archive changed %s %s %v", before, after, err)
	}
}
