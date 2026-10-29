package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/nativeid"
	sourceinfo "github.com/vzd3v/agent_session_collector/internal/source"
	"github.com/vzd3v/agent_session_collector/internal/vlevent"
)

func codexHeader(id string) string {
	return `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
}

func writeDescriptor(t *testing.T, cfg config.HubAppConfig, d sourceinfo.Descriptor) {
	t.Helper()
	body, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(cfg.Paths.Mirror, cfg.Receive.Sender[0].Mirror, sourceinfo.DescriptorPath(d.User, d.Agent))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentMetadataUpdatesArchivedOnlyWithoutChangingSnapshotOrClock(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	const id = "00000000-0000-4000-8000-000000000001"
	name := writeMirrorFile(t, cfg, "operator/codex/sessions/rollout-2026-10-29T00-00-00-"+id+".jsonl", codexHeader(id))
	setFileMtime(t, name, now.Add(-72*time.Hour))
	d := sourceinfo.Descriptor{Version: 1, Sender: "sender-alpha", User: "operator", Agent: "codex", HostName: "sender-alpha.example.net", Env: "test", DataClass: "internal_agent_transcript", Metadata: map[string]string{"team": "one"}}
	writeDescriptor(t, cfg, d)
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var archive, changed, key string
	if err := db.QueryRow(`SELECT latest_archive_path,last_changed_at,session_key FROM sessions`).Scan(&archive, &changed, &key); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(archive + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Force the one-time archived-only projection backfill without a new source session.
	if _, err := db.Exec(`UPDATE sessions SET projection_version=0,native_session_id='',identity_evidence_json='{}'`); err != nil {
		t.Fatal(err)
	}
	d.Metadata["team"] = "two"
	d.Env = "updated"
	writeDescriptor(t, cfg, d)
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now.Add(2 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	var gotChanged, env, labels, native, gotKey, status string
	var version int
	if err := db.QueryRow(`SELECT last_changed_at,env,metadata_json,native_session_id,projection_version,session_key,archive_status FROM sessions`).Scan(&gotChanged, &env, &labels, &native, &version, &gotKey, &status); err != nil {
		t.Fatal(err)
	}
	if gotChanged != changed || env != "updated" || labels != `{"team":"two"}` || native != id || version != nativeid.Version || gotKey != key || status != "archived" {
		t.Fatalf("metadata/backfill lost state: %s %s %s %s %d %s %s", gotChanged, env, labels, native, version, gotKey, status)
	}
	after, err := os.ReadFile(archive + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("archive-time snapshot was rewritten by current labels")
	}
	// Cache really avoids opening/decompressing the immutable archive on the next index.
	if err := os.Rename(archive, archive+".offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now.Add(3 * time.Hour) }}); err != nil {
		t.Fatalf("cached projection reread archive: %v", err)
	}
}

func TestNativeCompressedRolloutPreservesPhysicalBytesAndLogicalHash(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	now := time.Now().UTC()
	const id = "00000000-0000-4000-8000-000000000001"
	raw := []byte(codexHeader(id) + `{"type":"future_record","payload":{"anything":"preserve"}}` + "\n{partial")
	cmd := exec.Command("zstd", "-q", "-c")
	cmd.Stdin = bytes.NewReader(raw)
	compressed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	name := writeMirrorFile(t, cfg, "operator/codex/sessions/rollout-2026-10-29T00-00-00-"+id+".jsonl.zst", string(compressed))
	setFileMtime(t, name, now.Add(-72*time.Hour))
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var logical, physical, native, encoding string
	var rawSize, physicalSize int64
	if err := db.QueryRow(`SELECT latest_raw_sha256,latest_physical_sha256,latest_raw_size_bytes,latest_size_bytes,native_session_id,source_encoding FROM sessions`).Scan(&logical, &physical, &rawSize, &physicalSize, &native, &encoding); err != nil {
		t.Fatal(err)
	}
	rawHash := sha256.Sum256(raw)
	physicalHash := sha256.Sum256(compressed)
	if logical != hex.EncodeToString(rawHash[:]) || physical != hex.EncodeToString(physicalHash[:]) || rawSize != int64(len(raw)) || physicalSize != int64(len(compressed)) || native != id || encoding != "zstd" {
		t.Fatalf("incorrect compressed contract: %s %s %d %d %s %s", logical, physical, rawSize, physicalSize, native, encoding)
	}
	if _, err := Compact(ctx, cfg, CompactOptions{Now: func() time.Time { return now.Add(time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	var archive string
	if err := db.QueryRow(`SELECT latest_archive_path FROM sessions`).Scan(&archive); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, compressed) {
		t.Fatal("native zstd was recompressed or double-compressed")
	}
	var snapshot map[string]any
	body, err := os.ReadFile(archive + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["provenance"] != "archive_time" || snapshot["source_encoding"] != "zstd" {
		t.Fatalf("snapshot: %#v", snapshot)
	}
}

func TestInvalidDescriptorIsReportedWithoutBlockingHealthySource(t *testing.T) {
	cfg := testHubConfig(t.TempDir())
	ctx := context.Background()
	writeMirrorFile(t, cfg, "operator/claude/projects/project/opaque.jsonl", "unknown\n")
	writeMirrorFile(t, cfg, "operator/codex/sessions/opaque.jsonl", "unknown\n")
	writeDescriptor(t, cfg, sourceinfo.Descriptor{Version: 1, Sender: "sender-alpha", User: "operator", Agent: "codex", HostName: "sender.example.net", DataClass: "internal_agent_transcript"})
	path := filepath.Join(cfg.Paths.Mirror, cfg.Receive.Sender[0].Mirror, sourceinfo.DescriptorPath("operator", "codex"))
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Index(ctx, cfg, Options{})
	if err == nil || result.MetadataErrors != 1 || result.Upserted != 1 {
		t.Fatalf("source isolation: %#v %v", result, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var id string
	if err := db.QueryRow(`SELECT artifact_id FROM sessions WHERE agent='claude'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("missing artifact identity")
	}
}

func TestAbsentDescriptorRetainsAcceptedLabelsAndRevisionClock(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	now := time.Now().UTC()
	writeMirrorFile(t, cfg, "operator/codex/sessions/opaque.jsonl", "unknown\n")
	d := sourceinfo.Descriptor{Version: 1, Sender: "sender-alpha", User: "operator", Agent: "codex", HostName: "sender.example.net", Env: "test", DataClass: "internal_agent_transcript", Metadata: map[string]string{"team": "one"}}
	writeDescriptor(t, cfg, d)
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var initial string
	if err := db.QueryRow(`SELECT updated_at FROM sources`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	var current string
	if err := db.QueryRow(`SELECT updated_at FROM sources`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != initial {
		t.Fatal("unchanged descriptor renewed metadata revision clock")
	}
	if err := os.Remove(filepath.Join(cfg.Paths.Mirror, cfg.Receive.Sender[0].Mirror, sourceinfo.DescriptorPath(d.User, d.Agent))); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(ctx, cfg, Options{Now: func() time.Time { return now.Add(2 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	var host, env, labels, provenance string
	if err := db.QueryRow(`SELECT host_name,env,metadata_json,source_metadata_provenance FROM sessions`).Scan(&host, &env, &labels, &provenance); err != nil {
		t.Fatal(err)
	}
	if host != d.HostName || env != d.Env || labels != `{"team":"one"}` || provenance != "last_accepted_descriptor" {
		t.Fatalf("accepted metadata erased: %s %s %s %s", host, env, labels, provenance)
	}
}

func TestCorruptAndOversizedCompressedArtifactsDoNotBlockHealthyIndex(t *testing.T) {
	requireZstd(t)
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	cfg.Index.MaxDecodedFileSize = config.ByteSize{Bytes: 1024}
	writeMirrorFile(t, cfg, "operator/claude/projects/project/healthy.jsonl", "{}\n")
	writeMirrorFile(t, cfg, "operator/codex/sessions/corrupt.jsonl.zst", "not zstd")
	cmd := exec.Command("zstd", "-q", "-c")
	cmd.Stdin = bytes.NewReader(bytes.Repeat([]byte("x"), 2048))
	body, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	writeMirrorFile(t, cfg, "operator/codex/sessions/large.jsonl.zst", string(body))
	result, err := Index(ctx, cfg, Options{})
	if err == nil || result.Upserted != 1 || result.ArtifactErrors != 2 {
		t.Fatalf("decode isolation: %#v %v", result, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("unverified projections entered catalog")
	}
}

func TestChangedGenerationIsRetryableWithoutPublishingProjection(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	name := writeMirrorFile(t, cfg, "operator/codex/sessions/changing.jsonl", "{}\n")
	files, err := scanMirror(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Open/migrate through a normal pass, then invalidate the projection cache.
	if _, err := Index(ctx, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	if _, err := db.Exec(`UPDATE sessions SET projection_version=0`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareArtifact(ctx, db, &files[0], decodedFileLimit(cfg)); !errors.Is(err, errArtifactChanged) {
		t.Fatalf("changed generation not retryable: %v", err)
	}
}

func TestTelemetryFailurePersistsCatalogAndRetriesWithoutRenewingContentClock(t *testing.T) {
	ctx := context.Background()
	cfg := testHubConfig(t.TempDir())
	now := time.Now().UTC()
	requests := 0
	var delivered vlevent.AgentSessionIndex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&delivered); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg.Index.EmitVLEvents = true
	cfg.Index.VLEndpoint = server.URL
	writeMirrorFile(t, cfg, "operator/codex/sessions/opaque.jsonl", "unknown\n")
	result, err := Index(ctx, cfg, Options{Now: func() time.Time { return now }, HTTPClient: server.Client()})
	if err == nil || result.Upserted != 1 || result.EventsEmitted != 0 {
		t.Fatalf("failed telemetry lost catalog: %#v %v", result, err)
	}
	db := openCatalog(t, cfg.Paths.Catalog)
	var changed, ack string
	if err := db.QueryRow(`SELECT last_changed_at,last_index_event_raw_sha256 FROM sessions`).Scan(&changed, &ack); err != nil {
		t.Fatal(err)
	}
	if changed != now.Format(time.RFC3339Nano) || ack != "" {
		t.Fatal("failed telemetry was acknowledged")
	}
	result, err = Index(ctx, cfg, Options{Now: func() time.Time { return now.Add(time.Hour) }, HTTPClient: server.Client()})
	if err != nil || result.EventsEmitted != 1 || requests != 2 {
		t.Fatalf("pending content not retried: %#v %v", result, err)
	}
	var after string
	if err := db.QueryRow(`SELECT last_changed_at,last_index_event_raw_sha256 FROM sessions`).Scan(&after, &ack); err != nil {
		t.Fatal(err)
	}
	if after != changed || ack == "" || !delivered.FileChanged || delivered.FileChangedAt != changed || delivered.SchemaVersion != 2 || delivered.SessionID != "" || delivered.ArtifactID == "" {
		t.Fatalf("retry contract: changed=%s after=%s ack=%s event=%#v", changed, after, ack, delivered)
	}
}
