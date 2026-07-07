package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
)

func TestCompactReplaceArchivesAndDeletesMirrorWhenRetentionZero(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Retention.DeleteMirrorAfterArchive = true
	cfg.Archive.RetainPlainAfterArchive = config.Duration{}
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	old := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, old)
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	indexFixture(t, cfg, now)

	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if result.Archived != 1 || result.DeletedMirror != 1 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(mirrorPath); !os.IsNotExist(err) {
		t.Fatalf("mirror file still exists or unexpected error: %v", err)
	}

	db := openCatalog(t, cfg.Paths.Catalog)
	var status, archivePath, archiveSHA string
	if err := db.QueryRow(`SELECT archive_status, latest_archive_path, latest_archive_sha256 FROM sessions`).Scan(&status, &archivePath, &archiveSHA); err != nil {
		t.Fatal(err)
	}
	if status != "archived" || archivePath == "" || archiveSHA == "" {
		t.Fatalf("archive fields status=%q path=%q sha=%q", status, archivePath, archiveSHA)
	}
	if err := exec.Command("zstd", "-q", "-t", archivePath).Run(); err != nil {
		t.Fatalf("zstd verification failed: %v", err)
	}
}

func TestCompactSkipsFilesNewerThanQuietThreshold(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, now.Add(-time.Hour))
	indexFixture(t, cfg, now)

	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if result.Archived != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCompactRejectsChecksumMismatch(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "aaaaaa\n")
	old := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, old)
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	indexFixture(t, cfg, now)
	if err := os.WriteFile(mirrorPath, []byte("bbbbbb\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, mirrorPath, old)

	_, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompactVersionedArchives(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Archive.Policy = "versioned"
	cfg.Archive.VersionRetention = 10
	rel := "operator/claude/projects/synthetic-project/claude-session-0001.jsonl"
	mirrorPath := writeMirrorFile(t, cfg, rel, "claude-v1\n")
	old := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, old)
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first Compact returned error: %v", err)
	}

	if err := os.WriteFile(mirrorPath, []byte("claude-v2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, mirrorPath, old.Add(time.Hour))
	indexFixture(t, cfg, now.Add(2*time.Hour))
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(2 * time.Hour) }}); err != nil {
		t.Fatalf("second Compact returned error: %v", err)
	}

	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM session_archives`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("archive version count=%d, want 2", count)
	}
}

func TestCompactReplaceReusesExistingArchiveForSameRawHash(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	rel := "operator/claude/projects/synthetic-project/claude-session-0001.jsonl"
	mirrorPath := writeMirrorFile(t, cfg, rel, "same-content\n")
	old := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, old)
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first Compact returned error: %v", err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var archivePath string
	if err := db.QueryRow(`SELECT latest_archive_path FROM sessions`).Scan(&archivePath); err != nil {
		t.Fatal(err)
	}
	archiveTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	setFileMtime(t, archivePath, archiveTime)

	setFileMtime(t, mirrorPath, old.Add(time.Hour))
	indexFixture(t, cfg, now.Add(2*time.Hour))
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(2 * time.Hour) }}); err != nil {
		t.Fatalf("second Compact returned error: %v", err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(archiveTime) {
		t.Fatalf("archive was overwritten, mtime=%s want %s", info.ModTime(), archiveTime)
	}
}

func TestIndexPreservesArchivedStateForUnchangedRetainedMirror(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Retention.DeleteMirrorAfterArchive = false
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	old := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, old)
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	indexFixture(t, cfg, now.Add(time.Hour))

	db := openCatalog(t, cfg.Paths.Catalog)
	var status string
	if err := db.QueryRow(`SELECT archive_status FROM sessions`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Fatalf("archive_status=%q", status)
	}
}

func TestCompactRespectsMirrorDeletionRetainWindow(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Retention.DeleteMirrorAfterArchive = true
	cfg.Archive.RetainPlainAfterArchive = config.Duration{Duration: 48 * time.Hour}
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, now.Add(-25*time.Hour))
	indexFixture(t, cfg, now)

	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if result.Archived != 1 || result.DeletedMirror != 0 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(mirrorPath); err != nil {
		t.Fatalf("mirror file should remain during retain window: %v", err)
	}
}

func TestCompactDeletesRetainedMirrorOnLaterRun(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Retention.DeleteMirrorAfterArchive = true
	cfg.Archive.RetainPlainAfterArchive = config.Duration{Duration: 48 * time.Hour}
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, now.Add(-25*time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first Compact returned error: %v", err)
	}

	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(49 * time.Hour) }})
	if err != nil {
		t.Fatalf("second Compact returned error: %v", err)
	}
	if result.Archived != 0 || result.DeletedMirror != 1 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(mirrorPath); !os.IsNotExist(err) {
		t.Fatalf("mirror file still exists or unexpected error: %v", err)
	}
}

func TestCompactDoesNotDeleteRetainedMirrorChangedBeforeIndex(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	cfg.Retention.DeleteMirrorAfterArchive = true
	cfg.Archive.RetainPlainAfterArchive = config.Duration{Duration: 48 * time.Hour}
	mirrorPath := writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude-v1\n")
	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, now.Add(-25*time.Hour))
	indexFixture(t, cfg, now)
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first Compact returned error: %v", err)
	}
	if err := os.WriteFile(mirrorPath, []byte("claude-v2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, mirrorPath, now.Add(-24*time.Hour))

	result, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(49 * time.Hour) }})
	if err != nil {
		t.Fatalf("second Compact returned error: %v", err)
	}
	if result.DeletedMirror != 0 {
		t.Fatalf("result = %#v", result)
	}
	body, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatalf("mirror file should remain: %v", err)
	}
	if string(body) != "claude-v2\n" {
		t.Fatalf("mirror content = %q", body)
	}
}

func TestArchivePathIncludesStableHashToAvoidSanitizedCollision(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	a := compactSession{Sender: "sender-alpha", User: "operator", Agent: "claude", SessionID: "a:b", SessionKey: "claude:sender-alpha:operator:a:b", LatestRawSHA256: strings.Repeat("a", 64)}
	b := compactSession{Sender: "sender-alpha", User: "operator", Agent: "claude", SessionID: "a?b", SessionKey: "claude:sender-alpha:operator:a?b", LatestRawSHA256: strings.Repeat("a", 64)}
	if archivePath(cfg, a, 0) == archivePath(cfg, b, 0) {
		t.Fatalf("archive paths collided: %q", archivePath(cfg, a, 0))
	}
}

func TestVerifyZstdChecksDecompressedRawSHA256(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	rawPath := filepath.Join(tmp, "raw.jsonl")
	archivePath := filepath.Join(tmp, "raw.jsonl.zst")
	if err := os.WriteFile(rawPath, []byte("raw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("zstd", "-q", "-f", "-o", archivePath, rawPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zstd failed: %v %s", err, output)
	}
	err := verifyZstd(context.Background(), archivePath, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "archive raw checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func requireZstd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skipf("zstd not installed: %v", err)
	}
}

func indexFixture(t *testing.T, cfg config.HubAppConfig, now time.Time) {
	t.Helper()
	if _, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Index returned error: %v", err)
	}
}

func setFileMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
