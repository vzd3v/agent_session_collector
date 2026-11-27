package readapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/internal/source"
	"github.com/vzd3v/agent_session_collector/sessionapi"
)

const (
	testToken       = "synthetic_example_token_only_for_test_1"
	testReadToken   = "synthetic_example_token_only_for_test_2"
	testTranscript  = "{\"type\":\"user\",\"text\":\"one\"}\n{\"type\":\"assistant\",\"text\":\"two\"}\n{\"partial\":"
	testNativeID    = "00000000-0000-4000-8000-000000000001"
	testRootRecord  = "projects/example/" + testNativeID + ".jsonl"
	testChildRecord = "projects/example/" + testNativeID + "/subagents/agent-example.jsonl"
)

type fixture struct {
	cfg        Config
	db         *sql.DB
	server     *httptest.Server
	first      string
	second     string
	client     *sessionapi.Client
	readClient *sessionapi.Client
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		cfg: Config{
			Catalog: filepath.Join(dir, "catalog.sqlite"), Mirror: filepath.Join(dir, "mirror"),
			Archive: filepath.Join(dir, "archive"), TokensFile: filepath.Join(dir, "tokens.json"),
			MaxBytes: 32 << 20, MaxTransfers: 2,
		},
		first:  sessionapi.SourceID("synthetic-one", "operator", "claude"),
		second: sessionapi.SourceID("synthetic-two", "operator", "claude"),
	}
	var err error
	f.db, err = schema.Open(f.cfg.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := schema.Apply(t.Context(), f.db, schema.Hub); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.cfg.Mirror, f.cfg.Archive} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exp := time.Now().Add(time.Hour)
	tokens := []token{
		{SHA256: digest(testToken), ExpiresAt: exp, ReadSources: []string{f.first}, ExportSources: []string{f.first}},
		{SHA256: digest(testReadToken), ExpiresAt: exp, ReadSources: []string{f.first}},
	}
	writeJSONFile(t, f.cfg.TokensFile, tokens)
	api, err := New(t.Context(), f.cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	f.server = httptest.NewServer(api.Handler())
	t.Cleanup(f.server.Close)
	f.client, err = sessionapi.NewClient(f.server.URL, testToken, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.readClient, err = sessionapi.NewClient(f.server.URL, testReadToken, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func writeJSONFile(t *testing.T, name string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digest(v string) string {
	d := sha256.Sum256([]byte(v))
	return hex.EncodeToString(d[:])
}

func (f fixture) add(t *testing.T, src, name, role, content string) (sessionapi.Ref, string) {
	t.Helper()
	ref := sessionapi.Ref{SourceID: src, ArtifactID: source.ArtifactID(src, name), SHA256: digest(content)}
	filename := filepath.Join(f.cfg.Mirror, src, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.db.Exec(`INSERT INTO sessions
(session_key,agent,sender,user,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,
 latest_raw_sha256,archive_status,first_seen_at,last_indexed_at,source_id,artifact_id,
 relative_path,artifact_role,latest_physical_sha256,latest_raw_size_bytes,native_session_id,native_root_id)
VALUES(?, 'claude', 'synthetic', 'operator', ?, ?, ?, 0, ?, 'hot','2026-01-01T00:00:00Z',
 '2026-01-01T00:00:00Z', ?, ?, ?, ?, ?, ?, ?, ?)`,
		ref.ArtifactID, testNativeID, filename, len(content), ref.SHA256, src, ref.ArtifactID,
		name, role, ref.SHA256, len(content), testNativeID, testNativeID)
	if err != nil {
		t.Fatal(err)
	}
	return ref, filename
}

func requireStatus(t *testing.T, got error, expected int) {
	t.Helper()
	var response *sessionapi.HTTPError
	if !errors.As(got, &response) || response.Status != expected {
		t.Fatalf("error=%v, want HTTP %d", got, expected)
	}
}

func TestScopePagingAndRevocation(t *testing.T) {
	f := newFixture(t)
	a, _ := f.add(t, f.first, testRootRecord, "transcript", testTranscript)
	b, _ := f.add(t, f.first, testChildRecord, "transcript", "{}\n")
	hidden, _ := f.add(t, f.second, testRootRecord, "transcript", "{}\n")

	one, err := f.client.List(t.Context(), sessionapi.Query{Limit: 1})
	if err != nil || len(one.Sessions) != 1 || one.Sessions[0].SourceID != f.first || one.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", one, err)
	}
	two, err := f.client.List(t.Context(), sessionapi.Query{Limit: 1, Cursor: one.NextCursor})
	if err != nil || len(two.Sessions) != 1 || two.Sessions[0].SourceID != f.first || two.Sessions[0].ArtifactID == one.Sessions[0].ArtifactID || two.NextCursor != "" {
		t.Fatalf("second page=%+v err=%v", two, err)
	}
	empty, err := f.client.List(t.Context(), sessionapi.Query{SourceIDs: []string{f.second}})
	if err != nil || len(empty.Sessions) != 0 {
		t.Fatalf("disallowed list=%+v err=%v", empty, err)
	}
	_, err = f.client.List(t.Context(), sessionapi.Query{SourceIDs: []string{f.second}, Limit: 1, Cursor: one.NextCursor})
	requireStatus(t, err, 400)
	_, err = f.client.Get(t.Context(), hidden)
	requireStatus(t, err, 404)
	hidden.SourceID = f.first
	_, err = f.client.Get(t.Context(), hidden)
	requireStatus(t, err, 404)
	_, err = f.readClient.Open(t.Context(), a, sessionapi.Transcript, "gzip")
	requireStatus(t, err, 404)

	events, err := f.readClient.Read(t.Context(), sessionapi.ReadQuery{Ref: a, Limit: 1})
	if err != nil || len(events.Records) != 1 || events.NextCursor == "" || events.SHA256 != a.SHA256 {
		t.Fatalf("first records=%+v err=%v", events, err)
	}
	_, err = f.readClient.Read(t.Context(), sessionapi.ReadQuery{Ref: b, Cursor: events.NextCursor})
	requireStatus(t, err, 400)
	last, err := f.readClient.Read(t.Context(), sessionapi.ReadQuery{Ref: a, Cursor: events.NextCursor})
	if err != nil || len(last.Records) != 1 || !last.IncompleteTail || last.NextCursor != "" {
		t.Fatalf("final records=%+v err=%v", last, err)
	}

	replacement := f.cfg.TokensFile + ".next"
	writeJSONFile(t, replacement, []token{})
	if err := os.Rename(replacement, f.cfg.TokensFile); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.Get(t.Context(), a)
	requireStatus(t, err, 401)
}

func TestExportsVerifyGenerationAndBundleMembership(t *testing.T) {
	f := newFixture(t)
	a, filename := f.add(t, f.first, testRootRecord, "transcript", testTranscript)
	child, _ := f.add(t, f.first, testChildRecord, "transcript", "{}\n")
	f.add(t, f.first, "projects/other/"+testNativeID+".jsonl", "transcript", "{}\n")
	f.add(t, f.first, "projects/example/tool-results/result.json", "tool_result", "{}\n")
	_, outside := f.add(t, f.second, testRootRecord, "transcript", testTranscript)
	response, err := f.client.Open(t.Context(), a, sessionapi.Transcript, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Encoding") != "gzip" || response.Header.Get("X-Content-SHA256") != a.SHA256 {
		t.Fatalf("transcript headers=%v", response.Header)
	}
	transcript, err := io.ReadAll(gzipBody(t, response.Body))
	if err != nil || string(transcript) != testTranscript {
		t.Fatalf("bad transcript %q: %v", transcript, err)
	}

	response, err = f.client.Open(t.Context(), a, sessionapi.Archive, "")
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gzipBody(t, response.Body))
	header, err := reader.Next()
	if err != nil || header.Name != "manifest.json" {
		t.Fatalf("first member=%v err=%v", header, err)
	}
	var manifest manifest
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil || len(manifest.Artifacts) != 2 {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	actual := map[string]string{}
	for {
		header, err = reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		actual[header.Name] = digest(string(bytes))
	}
	if len(actual) != 2 || actual[a.ArtifactID+".jsonl"] != a.SHA256 || actual[child.ArtifactID+".jsonl"] != child.SHA256 {
		t.Fatalf("archive members=%v", actual)
	}
	if err := os.WriteFile(filename, []byte(strings.Repeat("x", len(testTranscript))), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.Open(t.Context(), a, sessionapi.Transcript, "")
	requireStatus(t, err, 409)
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filename); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.Open(t.Context(), a, sessionapi.Transcript, "")
	requireStatus(t, err, 409)
}

func TestReadArchiveOnlyAndRejectStaleArchive(t *testing.T) {
	zstd, err := exec.LookPath("zstd")
	if err != nil {
		t.Skip("zstd is required for this test")
	}
	f := newFixture(t)
	ref, mirror := f.add(t, f.first, testRootRecord, "transcript", testTranscript)
	cmd := exec.CommandContext(t.Context(), zstd, "-q", "-c")
	cmd.Stdin = strings.NewReader(testTranscript)
	compressed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	arch := filepath.Join(f.cfg.Archive, "example.jsonl.zst")
	if err := os.WriteFile(arch, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := digest(string(compressed))
	if _, err := f.db.Exec(`INSERT INTO session_archives(session_key,version,path,raw_size_bytes,raw_mtime_unix_ns,raw_sha256,archive_sha256,created_at)
VALUES (?,0,?,?,0,?,?, '2026-01-01T00:00:00Z')`, ref.ArtifactID, arch, len(testTranscript), ref.SHA256, sum); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET latest_archive_path=?,latest_archive_sha256=? WHERE artifact_id=?", arch, sum, ref.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mirror); err != nil {
		t.Fatal(err)
	}
	page, err := f.client.Read(t.Context(), sessionapi.ReadQuery{Ref: ref, Limit: 1})
	if err != nil || len(page.Records) != 1 || page.NextCursor == "" {
		t.Fatalf("archive page=%+v err=%v", page, err)
	}
	// An old archive pointer must not supply bytes for a newer indexed entry.
	if _, err := f.db.Exec("UPDATE sessions SET latest_raw_sha256=? WHERE artifact_id=?", digest("replacement"), ref.ArtifactID); err != nil {
		t.Fatal(err)
	}
	ref.SHA256 = ""
	_, err = f.client.Read(t.Context(), sessionapi.ReadQuery{Ref: ref})
	requireStatus(t, err, 409)
}

func gzipBody(t *testing.T, body io.ReadCloser) io.Reader {
	t.Helper()
	t.Cleanup(func() { _ = body.Close() })
	gz, err := gzip.NewReader(body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gz.Close() })
	return gz
}

func TestReadConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "read-api.toml")
	var b bytes.Buffer
	fmt.Fprintln(&b, "catalog = '/catalog.sqlite'\nmirror = '/mirror'\narchive = '/archive'\ntokens_file = '/auth/tokens.json'\nlisten = '127.0.0.1:8765'\ntimeout = '90s'")
	if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(file)
	if err != nil || cfg.Timeout != 90*time.Second {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}
