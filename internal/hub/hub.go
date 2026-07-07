package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/checksum"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/internal/sessionid"
	"github.com/vzd3v/agent_session_collector/internal/vlevent"
)

const (
	archiveStatusHot = constants.ArchiveStatusHot
	serviceName      = constants.ServiceName
)

type Options struct {
	Now        func() time.Time
	RunID      string
	HTTPClient *http.Client
}

type IndexResult struct {
	Scanned       int
	Upserted      int
	EventsEmitted int
}

type mirrorFile struct {
	Sender         string
	User           string
	Agent          string
	SessionID      string
	ConversationID string
	MirrorPath     string
	SizeBytes      int64
	MtimeUnixNS    int64
	RawSHA256      string
}

type existingSession struct {
	SessionKey          string
	FirstSeenAt         string
	LastIndexEventAt    string
	LatestSizeBytes     int64
	LatestMtimeUnixNS   int64
	LatestRawSHA256     string
	ArchiveStatus       string
	LatestArchivePath   string
	LatestArchiveSHA256 string
}

type pendingEvent struct {
	sessionKey string
	event      vlevent.AgentSessionIndex
}

func Index(ctx context.Context, cfg config.HubAppConfig, opts Options) (IndexResult, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("run-%d", now.UnixNano())
	}
	files, err := scanMirror(cfg)
	if err != nil {
		return IndexResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Paths.Catalog), 0o750); err != nil {
		return IndexResult{}, fmt.Errorf("create catalog dir: %w", err)
	}
	db, err := schema.Open(cfg.Paths.Catalog)
	if err != nil {
		return IndexResult{}, err
	}
	defer func() {
		_ = db.Close()
	}()
	if err := schema.Apply(ctx, db, schema.Hub); err != nil {
		return IndexResult{}, err
	}

	result := IndexResult{Scanned: len(files)}
	var events []pendingEvent
	nowText := now.Format(time.RFC3339Nano)
	for _, file := range files {
		sessionKey := buildSessionKey(file)
		existing, found, err := loadExistingSession(ctx, db, sessionKey)
		if err != nil {
			return IndexResult{}, err
		}
		changed := !found ||
			existing.LatestSizeBytes != file.SizeBytes ||
			existing.LatestMtimeUnixNS != file.MtimeUnixNS ||
			existing.LatestRawSHA256 != file.RawSHA256
		emit := shouldEmitIndexEvent(cfg, existing.LastIndexEventAt, changed, now)
		if emit {
			events = append(events, pendingEvent{
				sessionKey: sessionKey,
				event:      buildEvent(file, runID),
			})
		}
	}
	if len(events) > 0 {
		if err := emitEvents(ctx, cfg, opts.HTTPClient, events); err != nil {
			return IndexResult{}, err
		}
		result.EventsEmitted = len(events)
	}
	emitted := make(map[string]struct{}, len(events))
	for _, event := range events {
		emitted[event.sessionKey] = struct{}{}
	}
	for _, file := range files {
		sessionKey := buildSessionKey(file)
		existing, found, err := loadExistingSession(ctx, db, sessionKey)
		if err != nil {
			return IndexResult{}, err
		}
		firstSeenAt := nowText
		lastIndexEventAt := existing.LastIndexEventAt
		if found {
			firstSeenAt = existing.FirstSeenAt
		}
		if _, ok := emitted[sessionKey]; ok {
			lastIndexEventAt = nowText
		}
		archiveStatus, archivePath, archiveSHA256 := archiveStateForIndex(file, existing, found)
		if err := upsertSession(ctx, db, file, sessionKey, firstSeenAt, nowText, lastIndexEventAt, archiveStatus, archivePath, archiveSHA256); err != nil {
			return IndexResult{}, err
		}
		result.Upserted++
	}
	return result, nil
}

func scanMirror(cfg config.HubAppConfig) ([]mirrorFile, error) {
	var files []mirrorFile
	for _, sender := range cfg.Receive.Sender {
		root := filepath.Join(cfg.Paths.Mirror, sender.Mirror)
		err := filepath.WalkDir(root, func(localPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) && localPath == root {
					return nil
				}
				return fmt.Errorf("walk mirror %s: %w", localPath, walkErr)
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("stat mirror file %s: %w", localPath, err)
			}
			if !info.Mode().IsRegular() || filepath.Ext(localPath) != ".jsonl" {
				return nil
			}
			rel, err := filepath.Rel(root, localPath)
			if err != nil {
				return fmt.Errorf("relativize mirror file %s: %w", localPath, err)
			}
			parsed, err := parseMirrorPath(sender.SenderID, filepath.ToSlash(rel))
			if err != nil {
				return fmt.Errorf("parse mirror file %s: %w", localPath, err)
			}
			sum, err := checksum.FileSHA256(localPath)
			if err != nil {
				return fmt.Errorf("checksum mirror file %s: %w", localPath, err)
			}
			abs, err := filepath.Abs(localPath)
			if err != nil {
				return fmt.Errorf("absolute mirror path %s: %w", localPath, err)
			}
			parsed.MirrorPath = abs
			parsed.SizeBytes = info.Size()
			parsed.MtimeUnixNS = info.ModTime().UnixNano()
			parsed.RawSHA256 = sum
			files = append(files, parsed)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func parseMirrorPath(senderID, rel string) (mirrorFile, error) {
	clean := strings.TrimPrefix(path.Clean(rel), "./")
	parts := strings.Split(clean, "/")
	if len(parts) < 3 {
		return mirrorFile{}, fmt.Errorf("expected <user>/<agent>/<path>")
	}
	agent := parts[1]
	switch agent {
	case constants.AgentClaude, constants.AgentCodex:
	default:
		return mirrorFile{}, fmt.Errorf("unsupported agent %q", agent)
	}
	sessionID, err := sessionid.FromPath(agent, strings.Join(parts[2:], "/"))
	if err != nil {
		return mirrorFile{}, err
	}
	conversationID := ""
	if agent == constants.AgentCodex {
		conversationID = sessionID
	}
	return mirrorFile{
		Sender:         senderID,
		User:           parts[0],
		Agent:          agent,
		SessionID:      sessionID,
		ConversationID: conversationID,
	}, nil
}

func loadExistingSession(ctx context.Context, db *sql.DB, sessionKey string) (existingSession, bool, error) {
	var existing existingSession
	err := db.QueryRowContext(ctx, `SELECT session_key, first_seen_at, last_index_event_at, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256, archive_status, latest_archive_path, latest_archive_sha256
FROM sessions WHERE session_key = ?`, sessionKey).Scan(
		&existing.SessionKey,
		&existing.FirstSeenAt,
		&existing.LastIndexEventAt,
		&existing.LatestSizeBytes,
		&existing.LatestMtimeUnixNS,
		&existing.LatestRawSHA256,
		&existing.ArchiveStatus,
		&existing.LatestArchivePath,
		&existing.LatestArchiveSHA256,
	)
	if err == nil {
		return existing, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return existingSession{}, false, nil
	}
	return existingSession{}, false, fmt.Errorf("load existing session: %w", err)
}

func archiveStateForIndex(file mirrorFile, existing existingSession, found bool) (status string, archivePath string, archiveSHA256 string) {
	if found &&
		existing.ArchiveStatus == constants.ArchiveStatusArchived &&
		existing.LatestSizeBytes == file.SizeBytes &&
		existing.LatestMtimeUnixNS == file.MtimeUnixNS &&
		existing.LatestRawSHA256 == file.RawSHA256 {
		return existing.ArchiveStatus, existing.LatestArchivePath, existing.LatestArchiveSHA256
	}
	return constants.ArchiveStatusHot, "", ""
}

func upsertSession(ctx context.Context, db *sql.DB, file mirrorFile, sessionKey string, firstSeenAt string, lastSeenAt string, lastIndexEventAt string, archiveStatus string, archivePath string, archiveSHA256 string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO sessions (
session_key, agent, sender, user, env, host_name, session_id, conversation_id,
mirror_path, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256,
archive_status, latest_archive_path, latest_archive_sha256, first_seen_at,
last_seen_at, last_index_event_at
) VALUES (?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_key) DO UPDATE SET
mirror_path = excluded.mirror_path,
latest_size_bytes = excluded.latest_size_bytes,
latest_mtime_unix_ns = excluded.latest_mtime_unix_ns,
latest_raw_sha256 = excluded.latest_raw_sha256,
archive_status = excluded.archive_status,
latest_archive_path = excluded.latest_archive_path,
latest_archive_sha256 = excluded.latest_archive_sha256,
last_seen_at = excluded.last_seen_at,
last_index_event_at = excluded.last_index_event_at`,
		sessionKey,
		file.Agent,
		file.Sender,
		file.User,
		file.SessionID,
		file.ConversationID,
		file.MirrorPath,
		file.SizeBytes,
		file.MtimeUnixNS,
		file.RawSHA256,
		archiveStatus,
		archivePath,
		archiveSHA256,
		firstSeenAt,
		lastSeenAt,
		lastIndexEventAt,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func shouldEmitIndexEvent(cfg config.HubAppConfig, lastIndexEventAt string, changed bool, now time.Time) bool {
	if !cfg.Index.EmitVLEvents {
		return false
	}
	if lastIndexEventAt == "" || changed {
		return true
	}
	if cfg.Index.EmitOnUnchangedEvery.Duration == 0 {
		return false
	}
	last, err := time.Parse(time.RFC3339Nano, lastIndexEventAt)
	if err != nil {
		return true
	}
	return now.Sub(last) >= cfg.Index.EmitOnUnchangedEvery.Duration
}

func buildEvent(file mirrorFile, runID string) vlevent.AgentSessionIndex {
	return vlevent.AgentSessionIndex{
		EventName:      "agent_session_index",
		ServiceName:    serviceName,
		Agent:          file.Agent,
		SenderID:       file.Sender,
		OSUser:         file.User,
		SessionID:      file.SessionID,
		ConversationID: file.ConversationID,
		FileSizeBytes:  file.SizeBytes,
		FileMtime:      time.Unix(0, file.MtimeUnixNS).UTC(),
		ArchiveStatus:  archiveStatusHot,
		SyncStatus:     constants.SyncStatusMirrored,
		CollectorRunID: runID,
	}
}

func buildSessionKey(file mirrorFile) string {
	pathHash := sha256.Sum256([]byte(file.MirrorPath))
	return strings.Join([]string{file.Agent, file.Sender, file.User, file.SessionID, hex.EncodeToString(pathHash[:8])}, ":")
}

func emitEvents(ctx context.Context, cfg config.HubAppConfig, client *http.Client, events []pendingEvent) error {
	if !cfg.Index.EmitVLEvents || len(events) == 0 {
		return nil
	}
	if strings.TrimSpace(cfg.Index.VLEndpoint) == "" {
		return fmt.Errorf("index.vl_endpoint is required when index.emit_vl_events is true")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var body bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event.event)
		if err != nil {
			return fmt.Errorf("marshal VL event: %w", err)
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Index.VLEndpoint, &body)
	if err != nil {
		return fmt.Errorf("create VL request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post VL events: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post VL events returned %s", resp.Status)
	}
	return nil
}

type DoctorCheck struct {
	Name    string
	OK      bool
	Message string
}

func Doctor(cfg config.HubAppConfig) []DoctorCheck {
	checks := []DoctorCheck{
		lookPathCheck("rsync", "rsync"),
		pathExistsCheck("paths.root", cfg.Paths.Root),
		pathExistsCheck("paths.mirror", cfg.Paths.Mirror),
		pathExistsCheck("paths.extracted", cfg.Paths.Extracted),
		pathParentWritableCheck("paths.catalog", cfg.Paths.Catalog),
	}
	if cfg.Archive.Enabled {
		checks = append(checks,
			lookPathCheck("zstd", "zstd"),
			pathExistsCheck("paths.archive", cfg.Paths.Archive),
		)
	}
	return checks
}

func lookPathCheck(name, binary string) DoctorCheck {
	path, err := exec.LookPath(binary)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Message: "missing from PATH"}
	}
	return DoctorCheck{Name: name, OK: true, Message: path}
}

func pathExistsCheck(name, value string) DoctorCheck {
	info, err := os.Stat(value)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Message: err.Error()}
	}
	if !info.IsDir() {
		return DoctorCheck{Name: name, OK: false, Message: "not a directory"}
	}
	return DoctorCheck{Name: name, OK: true, Message: value}
}

func pathParentWritableCheck(name, value string) DoctorCheck {
	parent := filepath.Dir(value)
	info, err := os.Stat(parent)
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Message: err.Error()}
	}
	if !info.IsDir() {
		return DoctorCheck{Name: name, OK: false, Message: "parent is not a directory"}
	}
	testFile, err := os.CreateTemp(parent, ".asc-doctor-*")
	if err != nil {
		return DoctorCheck{Name: name, OK: false, Message: err.Error()}
	}
	nameOnDisk := testFile.Name()
	closeErr := testFile.Close()
	removeErr := os.Remove(nameOnDisk)
	if closeErr != nil {
		return DoctorCheck{Name: name, OK: false, Message: closeErr.Error()}
	}
	if removeErr != nil {
		return DoctorCheck{Name: name, OK: false, Message: removeErr.Error()}
	}
	return DoctorCheck{Name: name, OK: true, Message: parent}
}
