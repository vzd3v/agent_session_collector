package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/internal/vlevent"
)

func TestIndexCatalogRowsForClaudeAndCodex(t *testing.T) {
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude\n")
	writeMirrorFile(t, cfg, "operator/codex/sessions/2026/07/07/rollout-2026-07-07T18-00-00-00000000-0000-4000-8000-000000000001.jsonl", "codex\n")

	result, err := Index(context.Background(), cfg, Options{
		Now:   func() time.Time { return time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC) },
		RunID: "test-run",
	})
	if err != nil {
		t.Fatalf("Index returned error: %v", err)
	}
	if result.Scanned != 2 || result.Upserted != 2 || result.EventsEmitted != 0 {
		t.Fatalf("result = %#v", result)
	}

	db := openCatalog(t, cfg.Paths.Catalog)
	rows, err := db.Query(`SELECT agent, sender, user, session_id, conversation_id, archive_status, mirror_path FROM sessions ORDER BY agent`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	var got []string
	for rows.Next() {
		var agent, sender, user, sessionID, conversationID, archiveStatus, mirrorPath string
		if err := rows.Scan(&agent, &sender, &user, &sessionID, &conversationID, &archiveStatus, &mirrorPath); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(mirrorPath, "/home/") {
			t.Fatalf("catalog mirror path contains raw home path: %s", mirrorPath)
		}
		got = append(got, strings.Join([]string{agent, sender, user, sessionID, conversationID, archiveStatus}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude|sender-alpha|operator|claude-session-0001||hot",
		"codex|sender-alpha|operator|00000000-0000-4000-8000-000000000001|00000000-0000-4000-8000-000000000001|hot",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}
}

func TestIndexKeepsDistinctFilesWithSameSessionID(t *testing.T) {
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	writeMirrorFile(t, cfg, "operator/claude/projects/project-a/claude-session-0001.jsonl", "claude-a\n")
	writeMirrorFile(t, cfg, "operator/claude/projects/project-b/claude-session-0001.jsonl", "claude-b\n")

	result, err := Index(context.Background(), cfg, Options{Now: func() time.Time {
		return time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("Index returned error: %v", err)
	}
	if result.Scanned != 2 || result.Upserted != 2 {
		t.Fatalf("result = %#v", result)
	}

	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE sender = 'sender-alpha' AND user = 'operator' AND agent = 'claude' AND session_id = 'claude-session-0001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("duplicate session_id rows = %d, want 2", count)
	}
}

func TestIndexRetainsUnresolvedCodexArtifact(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	writeMirrorFile(t, cfg, "operator/codex/sessions/unrecognized.jsonl", "opaque native data\n")
	writeMirrorFile(t, cfg, "operator/claude/projects/alpha/known.jsonl", "claude\n")
	result, err := Index(context.Background(), cfg, Options{})
	if err != nil || result.Upserted != 2 {
		t.Fatalf("mixed-artifact index = %#v, %v", result, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var artifactID, conversationID string
	if err := db.QueryRow(`SELECT session_id,conversation_id FROM sessions WHERE agent='codex'`).Scan(&artifactID, &conversationID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifactID, "artifact-") || conversationID != "" {
		t.Fatalf("unresolved identity fabricated conversation: %q %q", artifactID, conversationID)
	}
}

func TestIndexBackfillsCodexConversationIDOnExistingRow(t *testing.T) {
	tmp := t.TempDir()
	cfg := testHubConfig(tmp)
	const sessionID = "00000000-0000-4000-8000-000000000001"
	writeMirrorFile(t, cfg, "operator/codex/sessions/2026/07/07/rollout-2026-07-07T18-00-00-"+sessionID+".jsonl", "codex\n")

	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	if _, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first Index returned error: %v", err)
	}

	db := openCatalog(t, cfg.Paths.Catalog)
	if _, err := db.Exec(`UPDATE sessions SET conversation_id = '' WHERE agent = 'codex'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }}); err != nil {
		t.Fatalf("second Index returned error: %v", err)
	}

	var conversationID string
	if err := db.QueryRow(`SELECT conversation_id FROM sessions WHERE agent = 'codex' AND session_id = ?`, sessionID).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if conversationID != sessionID {
		t.Fatalf("conversation_id = %q, want %q", conversationID, sessionID)
	}
}

func TestIndexEmitsVLForChangedAndHeartbeatOnly(t *testing.T) {
	requireZstd(t)
	tmp := t.TempDir()
	var received []vlevent.AgentSessionIndex
	var receivedMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readRequestBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		lines := strings.Split(strings.TrimSpace(body), "\n")
		for _, line := range lines {
			var event vlevent.AgentSessionIndex
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if event.EventName != "agent_session_index" || event.ServiceName != serviceName {
				http.Error(w, "unexpected event identity", http.StatusInternalServerError)
				return
			}
			if event.SyncStatus != constants.SyncStatusMirrored {
				http.Error(w, "unexpected sync status", http.StatusInternalServerError)
				return
			}
			if event.SessionID == "" || event.SenderID == "" || event.FileSizeBytes == 0 || event.CollectorRunID == "" {
				http.Error(w, "event missing required fields", http.StatusInternalServerError)
				return
			}
			receivedMu.Lock()
			received = append(received, event)
			receivedMu.Unlock()
		}
	}))
	defer server.Close()

	cfg := testHubConfig(tmp)
	cfg.Index.EmitVLEvents = true
	cfg.Index.VLEndpoint = server.URL
	cfg.Index.EmitOnUnchangedEvery = config.Duration{Duration: 24 * time.Hour}
	mirrorPath := writeMirrorFile(t, cfg, "operator/codex/sessions/2026/07/07/rollout-2026-07-07T18-00-00-00000000-0000-4000-8000-000000000001.jsonl", "codex\n")

	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	setFileMtime(t, mirrorPath, now.Add(-25*time.Hour))
	result, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now }, RunID: "run-1", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("first Index returned error: %v", err)
	}
	if result.EventsEmitted != 1 || receivedLen(&receivedMu, &received) != 1 {
		t.Fatalf("first result=%#v received=%d", result, receivedLen(&receivedMu, &received))
	}
	if received[0].ConversationID != received[0].SessionID {
		t.Fatalf("codex conversation_id = %q, want session_id %q", received[0].ConversationID, received[0].SessionID)
	}

	result, err = Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }, RunID: "run-2", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("second Index returned error: %v", err)
	}
	if result.EventsEmitted != 0 || receivedLen(&receivedMu, &received) != 1 {
		t.Fatalf("second result=%#v received=%d", result, receivedLen(&receivedMu, &received))
	}
	if _, err := Compact(context.Background(), cfg, CompactOptions{Now: func() time.Time { return now.Add(2 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}

	result, err = Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(25 * time.Hour) }, RunID: "run-3", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("third Index returned error: %v", err)
	}
	if result.EventsEmitted != 1 || receivedLen(&receivedMu, &received) != 2 {
		t.Fatalf("third result=%#v received=%d", result, receivedLen(&receivedMu, &received))
	}
	if !received[0].FileChanged || received[1].FileChanged || received[0].FileChangedAt != received[1].FileChangedAt {
		t.Fatalf("heartbeat changed artifact freshness: %#v", received)
	}
	if received[0].ArchiveStatus != constants.ArchiveStatusHot || received[1].ArchiveStatus != constants.ArchiveStatusArchived {
		t.Fatalf("heartbeat archive state = %#v", received)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var changedAt, indexedAt string
	if err := db.QueryRow(`SELECT last_changed_at,last_indexed_at FROM sessions`).Scan(&changedAt, &indexedAt); err != nil {
		t.Fatal(err)
	}
	if changedAt != now.Format(time.RFC3339Nano) || indexedAt != now.Add(25*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("changed=%q indexed=%q", changedAt, indexedAt)
	}
}

func TestIndexRetriesChangedEventAfterVLFailureBeforeHeartbeat(t *testing.T) {
	tmp := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := testHubConfig(tmp)
	cfg.Index.EmitVLEvents = true
	cfg.Index.VLEndpoint = server.URL
	cfg.Index.EmitOnUnchangedEvery = config.Duration{Duration: 24 * time.Hour}
	writeMirrorFile(t, cfg, "operator/claude/projects/synthetic-project/claude-session-0001.jsonl", "claude-v1\n")

	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	_, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now }, RunID: "run-1", HTTPClient: server.Client()})
	if err == nil {
		t.Fatalf("expected first index to fail")
	}

	result, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }, RunID: "run-2", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("retry Index returned error: %v", err)
	}
	if result.EventsEmitted != 1 || requests != 2 {
		t.Fatalf("retry result=%#v requests=%d", result, requests)
	}
}

func TestIndexRetriesPriorSuccessfulChangedEventAfterVLFailure(t *testing.T) {
	tmp := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := testHubConfig(tmp)
	cfg.Index.EmitVLEvents = true
	cfg.Index.VLEndpoint = server.URL
	cfg.Index.EmitOnUnchangedEvery = config.Duration{Duration: 24 * time.Hour}
	rel := "operator/claude/projects/synthetic-project/claude-session-0001.jsonl"
	writeMirrorFile(t, cfg, rel, "claude-v1\n")

	now := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	if _, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now }, RunID: "run-1", HTTPClient: server.Client()}); err != nil {
		t.Fatalf("initial Index returned error: %v", err)
	}
	writeMirrorFile(t, cfg, rel, "claude-v2\n")
	_, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }, RunID: "run-2", HTTPClient: server.Client()})
	if err == nil {
		t.Fatalf("expected changed index to fail")
	}

	result, err := Index(context.Background(), cfg, Options{Now: func() time.Time { return now.Add(2 * time.Hour) }, RunID: "run-3", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("changed retry Index returned error: %v", err)
	}
	if result.EventsEmitted != 1 || requests != 3 {
		t.Fatalf("retry result=%#v requests=%d", result, requests)
	}
}

func TestDoctorReportsMissingPathsClearly(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	checks := Doctor(cfg)
	var foundMissingMirror bool
	for _, check := range checks {
		if check.Name == "paths.mirror" && !check.OK && strings.Contains(check.Message, "no such file") {
			foundMissingMirror = true
		}
	}
	if !foundMissingMirror {
		t.Fatalf("doctor checks did not report missing mirror clearly: %#v", checks)
	}
}

func testHubConfig(root string) config.HubAppConfig {
	return config.HubAppConfig{
		HubID: "collector-hub-alpha",
		Paths: config.HubPaths{
			Root:      root,
			Mirror:    filepath.Join(root, "mirror"),
			Archive:   filepath.Join(root, "archive"),
			Catalog:   filepath.Join(root, "catalog", "sessions.sqlite"),
			Extracted: filepath.Join(root, "extracted"),
		},
		Receive: config.ReceiveConfig{
			MaxFileSize:       config.ByteSize{Bytes: 2 << 30},
			UnixNameMaxLength: 32,
			Sender: []config.ReceiveSender{
				{SenderID: "sender-alpha", SenderSlug: "sender-alpha", ReceiveUser: "upload_alpha", MirrorGroup: "mirror_alpha", Mirror: "sender-alpha"},
			},
		},
		DataClasses: map[string]config.DataClass{
			"internal_agent_transcript": {RawHubAllowed: []string{"collector-hub-alpha"}},
		},
		Index: config.IndexConfig{
			Interval:             config.Duration{Duration: 10 * time.Minute},
			EmitOnUnchangedEvery: config.Duration{Duration: 24 * time.Hour},
		},
		Archive: config.ArchiveConfig{
			Enabled:                 true,
			QuietAfter:              config.Duration{Duration: 24 * time.Hour},
			Compression:             "zstd",
			ZstdLevel:               6,
			Policy:                  "replace",
			RetainPlainAfterArchive: config.Duration{Duration: 48 * time.Hour},
		},
	}
}

func writeMirrorFile(t *testing.T, cfg config.HubAppConfig, rel string, body string) string {
	t.Helper()
	target := filepath.Join(cfg.Paths.Mirror, "sender-alpha", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return target
}

func openCatalog(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := schema.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func readRequestBody(r *http.Request) (string, error) {
	defer func() {
		_ = r.Body.Close()
	}()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func receivedLen(mu *sync.Mutex, values *[]vlevent.AgentSessionIndex) int {
	mu.Lock()
	defer mu.Unlock()
	return len(*values)
}
