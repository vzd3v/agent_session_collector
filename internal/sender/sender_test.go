package sender_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/internal/sender"
)

func TestSyncSkipsUnchangedAfterSuccessfulUpload(t *testing.T) {
	cfg := testConfig(t)
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")

	uploader := &fakeUploader{}
	result, err := sender.Sync(context.Background(), cfg, sender.Options{
		Uploader: uploader,
		Sleeper:  noSleep,
		Now:      fixedNow,
	})
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if result.Uploaded != 1 || uploader.attempts != 1 {
		t.Fatalf("initial sync uploaded=%d attempts=%d, want 1/1", result.Uploaded, uploader.attempts)
	}

	var out bytes.Buffer
	result, err = sender.Sync(context.Background(), cfg, sender.Options{
		DryRun: true,
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("dry-run sync: %v", err)
	}
	if result.Planned != 0 || result.Skipped != 1 {
		t.Fatalf("dry-run planned=%d skipped=%d, want 0/1\n%s", result.Planned, result.Skipped, out.String())
	}
	if !strings.Contains(out.String(), "SKIP agent=claude") {
		t.Fatalf("dry-run output does not show skip:\n%s", out.String())
	}
}

func TestSyncPlansChangedFile(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl")
	writeJSONL(t, path, "2026-07-07T00:00:00Z")
	if _, err := sender.Sync(context.Background(), cfg, sender.Options{Uploader: &fakeUploader{}, Sleeper: noSleep, Now: fixedNow}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	writeJSONL(t, path, "2026-07-07T00:01:00Z")
	mtime := time.Date(2026, 7, 7, 0, 1, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes changed file: %v", err)
	}

	var out bytes.Buffer
	result, err := sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run after change: %v", err)
	}
	if result.Planned != 1 || result.Skipped != 0 {
		t.Fatalf("dry-run planned=%d skipped=%d, want 1/0\n%s", result.Planned, result.Skipped, out.String())
	}
	if !strings.Contains(out.String(), "PLAN agent=claude") {
		t.Fatalf("dry-run output does not show plan:\n%s", out.String())
	}
}

func TestScanAppliesConfiguredExcludesAndJSONLOnly(t *testing.T) {
	cfg := testConfig(t)
	cfg.Exclude.Patterns = []string{".claude/projects/skip/**"}
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "keep", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "skip", "claude-session-0002.jsonl"), "2026-07-07T00:00:00Z")
	writeFile(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "keep", "notes.txt"), "not a transcript")
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "outside", "claude-session-0003.jsonl"), "2026-07-07T00:00:00Z")

	files, err := sender.Scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got, want := displayPaths(files), []string{".claude/projects/keep/claude-session-0001.jsonl"}; !slices.Equal(got, want) {
		t.Fatalf("scanned paths = %#v, want %#v", got, want)
	}
}

func TestScanExtractsClaudeAndCodexSessionIDs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Sources = append(cfg.Sources, config.SourceConfig{
		Agent:     constants.AgentCodex,
		User:      "operator",
		Home:      cfg.Sources[0].Home,
		HostName:  "sender-alpha.example.net",
		Env:       "sender-alpha/operator",
		DataClass: "internal_agent_transcript",
		Paths:     []string{".codex/sessions"},
	})
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".codex", "sessions", "2026", "07", "07", "rollout-2026-07-07T00-00-00-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.jsonl"), "2026-07-07T00:00:00Z")

	files, err := sender.Scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := map[string]string{}
	for _, file := range files {
		got[file.Agent] = file.SessionID
	}
	if got[constants.AgentClaude] != "claude-session-0001" {
		t.Fatalf("claude session id = %q", got[constants.AgentClaude])
	}
	if got[constants.AgentCodex] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("codex session id = %q", got[constants.AgentCodex])
	}
}

func TestRetryUpdatesStateOnlyAfterSuccessfulUpload(t *testing.T) {
	cfg := testConfig(t)
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")

	failing := &fakeUploader{failures: 1}
	_, err := sender.Sync(context.Background(), cfg, sender.Options{
		Uploader: failing,
		Sleeper:  noSleep,
		Now:      fixedNow,
	})
	if err == nil {
		t.Fatalf("expected upload failure")
	}
	if failing.attempts != 1 {
		t.Fatalf("failure attempts=%d, want 1", failing.attempts)
	}

	var out bytes.Buffer
	result, err := sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run after failed upload: %v", err)
	}
	if result.Planned != 1 || result.Skipped != 0 {
		t.Fatalf("state was updated after failure: planned=%d skipped=%d\n%s", result.Planned, result.Skipped, out.String())
	}

	cfg.Sync.MaxAttempts = 2
	retrying := &fakeUploader{failures: 1}
	result, err = sender.Sync(context.Background(), cfg, sender.Options{
		Uploader: retrying,
		Sleeper:  noSleep,
		Now:      fixedNow,
	})
	if err != nil {
		t.Fatalf("retrying sync: %v", err)
	}
	if result.Uploaded != 1 || retrying.attempts != 2 {
		t.Fatalf("retry result uploaded=%d attempts=%d, want 1/2", result.Uploaded, retrying.attempts)
	}

	out.Reset()
	result, err = sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run after successful retry: %v", err)
	}
	if result.Planned != 0 || result.Skipped != 1 {
		t.Fatalf("state not updated after success: planned=%d skipped=%d\n%s", result.Planned, result.Skipped, out.String())
	}
}

func TestSyncDoesNotRequireSessionEndHook(t *testing.T) {
	cfg := testConfig(t)
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")

	var out bytes.Buffer
	result, err := sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run without hook config: %v", err)
	}
	if result.Planned != 1 {
		t.Fatalf("planned=%d, want 1\n%s", result.Planned, out.String())
	}
}

func TestDryRunDoesNotCreateState(t *testing.T) {
	cfg := testConfig(t)
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")

	var out bytes.Buffer
	result, err := sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if result.Planned != 1 {
		t.Fatalf("planned=%d, want 1\n%s", result.Planned, out.String())
	}
	if _, err := os.Stat(cfg.Sync.StatePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created state path or stat failed: %v", err)
	}
}

func TestDryRunDoesNotMigrateExistingState(t *testing.T) {
	cfg := testConfig(t)
	writeJSONL(t, filepath.Join(cfg.Sources[0].Home, ".claude", "projects", "alpha", "claude-session-0001.jsonl"), "2026-07-07T00:00:00Z")
	db, err := schema.Open(cfg.Sync.StatePath)
	if err != nil {
		t.Fatalf("open bare sqlite db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated_marker (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create marker table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close bare sqlite db: %v", err)
	}

	if _, err := sender.Sync(context.Background(), cfg, sender.Options{DryRun: true, Stdout: &bytes.Buffer{}}); err != nil {
		t.Fatalf("dry-run with existing state: %v", err)
	}

	db, err = schema.OpenReadOnly(cfg.Sync.StatePath)
	if err != nil {
		t.Fatalf("open sqlite db read-only: %v", err)
	}
	defer func() { _ = db.Close() }()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'synced_files'`).Scan(&tables); err != nil {
		t.Fatalf("query synced_files table: %v", err)
	}
	if tables != 0 {
		t.Fatalf("dry-run migrated sender state")
	}
}

func TestFixtureScanReportsPeakRSS(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	fixtures := filepath.Join(repoRoot, "testdata", "fixtures")
	cfg := testConfig(t)
	cfg.Sources = []config.SourceConfig{
		{
			Agent:     constants.AgentClaude,
			User:      "operator",
			Home:      fixtures,
			HostName:  "sender-alpha.example.net",
			DataClass: "internal_agent_transcript",
			Paths:     []string{"claude/projects"},
		},
		{
			Agent:     constants.AgentCodex,
			User:      "operator",
			Home:      fixtures,
			HostName:  "sender-alpha.example.net",
			Env:       "sender-alpha/operator",
			DataClass: "internal_agent_transcript",
			Paths:     []string{"codex/sessions", "codex/archived_sessions"},
		},
	}
	files, err := sender.Scan(cfg)
	if err != nil {
		t.Fatalf("fixture scan: %v", err)
	}
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	t.Logf("fixture_scan_peak_rss_kb=%d files=%d", usage.Maxrss, len(files))
	if len(files) != 4 {
		t.Fatalf("fixture files=%d, want 4", len(files))
	}
}

func TestStageFileCopiesWithoutMutatingSourceInode(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	writeJSONL(t, source, "2026-07-07T00:00:00Z")
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	originalTime := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, originalTime, originalTime); err != nil {
		t.Fatalf("set source time: %v", err)
	}

	stage := t.TempDir()
	remotePath := "operator/claude/projects/alpha/claude-session-0001.jsonl"
	file := sender.File{
		LocalPath:   source,
		DisplayPath: ".claude/projects/alpha/claude-session-0001.jsonl",
		RemotePath:  remotePath,
		MtimeUnixNS: originalTime.UnixNano(),
		SizeBytes:   info.Size(),
		Device:      uint64(stat.Dev),
		Inode:       stat.Ino,
	}
	if err := sender.StageFileForTest(stage, file); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	info, err = os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	if !info.ModTime().Equal(originalTime) {
		t.Fatalf("source mtime changed to %s, want %s", info.ModTime(), originalTime)
	}
	stagedInfo, err := os.Stat(filepath.Join(stage, filepath.FromSlash(remotePath)))
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	stagedStat := stagedInfo.Sys().(*syscall.Stat_t)
	if uint64(stagedStat.Dev) == uint64(stat.Dev) && stagedStat.Ino == stat.Ino {
		t.Fatalf("staged file reused source inode")
	}
}

func TestStageFileRejectsSymlinkSwapAfterScan(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	target := filepath.Join(dir, "target.jsonl")
	writeJSONL(t, source, "2026-07-07T00:00:00Z")
	writeJSONL(t, target, "2026-07-07T00:00:01Z")
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	if err := os.Symlink(target, source); err != nil {
		t.Fatalf("symlink swap: %v", err)
	}
	err = sender.StageFileForTest(t.TempDir(), sender.File{
		LocalPath:   source,
		DisplayPath: ".claude/projects/alpha/claude-session-0001.jsonl",
		RemotePath:  "operator/claude/projects/alpha/claude-session-0001.jsonl",
		MtimeUnixNS: info.ModTime().UnixNano(),
		SizeBytes:   info.Size(),
		Device:      uint64(stat.Dev),
		Inode:       stat.Ino,
	})
	if err == nil || !strings.Contains(err.Error(), "open source") {
		t.Fatalf("expected symlink open rejection, got %v", err)
	}
}

func TestStageFileRejectsRegularFileSwapAfterScan(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	writeJSONL(t, source, "2026-07-07T00:00:00Z")
	oldTime := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, oldTime, oldTime); err != nil {
		t.Fatalf("set source time: %v", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	replacement := filepath.Join(dir, "replacement.jsonl")
	writeJSONL(t, replacement, "2026-07-07T00:00:01Z")
	if err := os.Rename(replacement, source); err != nil {
		t.Fatalf("rename replacement: %v", err)
	}

	err = sender.StageFileForTest(t.TempDir(), sender.File{
		LocalPath:   source,
		DisplayPath: ".claude/projects/alpha/claude-session-0001.jsonl",
		RemotePath:  "operator/claude/projects/alpha/claude-session-0001.jsonl",
		MtimeUnixNS: info.ModTime().UnixNano(),
		SizeBytes:   info.Size(),
		Device:      uint64(stat.Dev),
		Inode:       stat.Ino,
	})
	if err == nil || !strings.Contains(err.Error(), "source changed since scan") {
		t.Fatalf("expected regular-file swap rejection, got %v", err)
	}
}

func TestStageFileRejectsZeroByteGrowthAfterScan(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.jsonl")
	writeFile(t, source, "")
	oldTime := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, oldTime, oldTime); err != nil {
		t.Fatalf("set source time: %v", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if err := os.WriteFile(source, []byte("grown\n"), 0o600); err != nil {
		t.Fatalf("grow source: %v", err)
	}
	err = sender.StageFileForTest(t.TempDir(), sender.File{
		LocalPath:   source,
		DisplayPath: ".claude/projects/alpha/claude-session-0001.jsonl",
		RemotePath:  "operator/claude/projects/alpha/claude-session-0001.jsonl",
		MtimeUnixNS: info.ModTime().UnixNano(),
		SizeBytes:   info.Size(),
		Device:      uint64(stat.Dev),
		Inode:       stat.Ino,
	})
	if err == nil || !strings.Contains(err.Error(), "source changed since scan") {
		t.Fatalf("expected zero-byte growth rejection, got %v", err)
	}
}

type fakeUploader struct {
	failures int
	attempts int
	uploads  [][]sender.File
}

func (u *fakeUploader) Upload(_ context.Context, files []sender.File) error {
	u.attempts++
	u.uploads = append(u.uploads, slices.Clone(files))
	if u.failures > 0 {
		u.failures--
		return errors.New("transient upload failure")
	}
	return nil
}

func testConfig(t *testing.T) config.SenderConfig {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "operator")
	return config.SenderConfig{
		SenderID: "sender-alpha",
		Hub: config.HubConfig{
			SSHTarget:     "upload_alpha@collector.example.net",
			RemoteCommand: "asc-receive",
		},
		Sync: config.SyncConfig{
			Interval:    config.Duration{Duration: 10 * time.Minute},
			StatePath:   filepath.Join(t.TempDir(), "sender.sqlite"),
			RsyncPath:   "/usr/bin/rsync",
			MaxAttempts: 1,
			Backoff:     config.Duration{Duration: time.Millisecond},
		},
		Sources: []config.SourceConfig{
			{
				Agent:     constants.AgentClaude,
				User:      "operator",
				Home:      home,
				HostName:  "sender-alpha.example.net",
				DataClass: "internal_agent_transcript",
				Paths:     []string{".claude/projects"},
			},
		},
		Exclude: config.ExcludeConfig{
			Patterns: []string{},
		},
	}
}

func writeJSONL(t *testing.T, path, timestamp string) {
	t.Helper()
	writeFile(t, path, fmt.Sprintf(`{"ts":%q,"message":"synthetic fixture"}`+"\n", timestamp))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func displayPaths(files []sender.File) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.DisplayPath)
	}
	slices.Sort(out)
	return out
}

func noSleep(context.Context, time.Duration) error {
	return nil
}

func fixedNow() time.Time {
	return time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
}
