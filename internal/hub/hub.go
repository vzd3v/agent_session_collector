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

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/checksum"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/nativeid"
	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/internal/sessionid"
	sourcemetadata "github.com/vzd3v/agent_session_collector/internal/source"
	"github.com/vzd3v/agent_session_collector/internal/vlevent"
)

const serviceName = constants.ServiceName

type Options struct {
	Now        func() time.Time
	RunID      string
	HTTPClient *http.Client
}

type IndexResult struct {
	Skipped        int
	ArtifactErrors int
	MetadataErrors int
	Scanned        int
	Upserted       int
	EventsEmitted  int
}

type mirrorFile struct {
	RelativePath            string
	ArtifactID              string
	SourceID                string
	Role                    string
	Encoding                string
	PhysicalSHA256          string
	RawSizeBytes            int64
	Descriptor              sourcemetadata.Descriptor
	MetadataSHA256          string
	LastEventMetadataSHA256 string
	LastEventRawSHA256      string
	Native                  nativeid.Projection
	LegacyKey               string
	ArchivedOnly            bool
	Sender                  string
	User                    string
	Agent                   string
	SessionID               string
	ConversationID          string
	MirrorPath              string
	SizeBytes               int64
	MtimeUnixNS             int64
	RawSHA256               string
}

type existingSession struct {
	LatestPhysicalSHA256 string
	SessionKey           string
	FirstSeenAt          string
	LastIndexEventAt     string
	LastChangedAt        string
	LatestSizeBytes      int64
	LatestMtimeUnixNS    int64
	LatestRawSHA256      string
	ArchiveStatus        string
	LatestArchivePath    string
	LatestArchiveSHA256  string
}

type pendingEvent struct {
	sessionKey     string
	rawSHA256      string
	metadataSHA256 string
	event          vlevent.AgentSessionIndex
}

func Index(ctx context.Context, cfg config.HubAppConfig, opts Options) (IndexResult, error) {
	unlock, err := lockHub(ctx, cfg.Paths.Catalog)
	if err != nil {
		return IndexResult{}, err
	}
	defer unlock()
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("run-%d", now.UnixNano())
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
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		return IndexResult{}, err
	}
	if err := recoverExpiryGroups(ctx, db, cfg); err != nil {
		return IndexResult{}, err
	}
	files, scanErr := scanMirror(cfg)

	result := IndexResult{Scanned: len(files)}
	sources, invalid, metadataErrors := indexSourceDescriptors(ctx, db, cfg, now)
	result.MetadataErrors = len(metadataErrors)
	if scanErr != nil {
		metadataErrors = append(metadataErrors, scanErr)
		result.ArtifactErrors++
	}
	var enriched []mirrorFile
	for _, file := range files {
		if invalid[file.SourceID] {
			continue
		}
		if descriptor, ok := sources[file.SourceID]; ok {
			file.Descriptor = descriptor
			body, _ := descriptor.Encode()
			file.MetadataSHA256 = sourcemetadata.Digest(body)
		}
		if err := prepareArtifact(ctx, db, &file, decodedFileLimit(cfg)); err != nil {
			if errors.Is(err, errArtifactChanged) {
				result.Skipped++
				continue
			}
			metadataErrors = append(metadataErrors, err)
			result.ArtifactErrors++
			continue
		}
		rejected, err := rejectExpiredReplay(ctx, db, cfg, file, now)
		if err != nil {
			return result, err
		}
		if rejected {
			result.Skipped++
			continue
		}
		enriched = append(enriched, file)
	}
	archived, err := archivedOnlyArtifacts(ctx, db, cfg, sources, invalid)
	if err != nil {
		metadataErrors = append(metadataErrors, err)
		result.ArtifactErrors++
	}
	files = append(enriched, archived...)
	var events []pendingEvent
	nowText := now.Format(time.RFC3339Nano)
	for _, file := range files {
		sessionKey := buildSessionKey(file)
		existing, found, err := loadExistingSession(ctx, db, sessionKey)
		if err != nil {
			return IndexResult{}, err
		}
		changed := mirrorChanged(file, existing, found)
		pendingContent := file.RawSHA256 != file.LastEventRawSHA256
		emit := shouldEmitIndexEvent(cfg, existing.LastIndexEventAt, changed || pendingContent || file.MetadataSHA256 != file.LastEventMetadataSHA256, now)
		if emit {
			archiveStatus, _, _ := archiveStateForIndex(file, existing, found)
			changedAt := existing.LastChangedAt
			if changed {
				changedAt = now.Format(time.RFC3339Nano)
			}
			events = append(events, pendingEvent{
				sessionKey: sessionKey,
				rawSHA256:  file.RawSHA256, metadataSHA256: file.MetadataSHA256,
				event: buildEvent(file, runID, archiveStatus, changed || pendingContent, changedAt, now),
			})
		}
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
		archiveStatus, archivePath, archiveSHA256 := archiveStateForIndex(file, existing, found)
		changedAt := existing.LastChangedAt
		if mirrorChanged(file, existing, found) {
			changedAt = nowText
		}
		if err := upsertSession(ctx, db, file, sessionKey, firstSeenAt, nowText, changedAt, lastIndexEventAt, archiveStatus, archivePath, archiveSHA256); err != nil {
			return IndexResult{}, err
		}
		result.Upserted++
		if err := storeArtifactMetadata(ctx, db, file, sessionKey); err != nil {
			return result, err
		}
		if file.ArchivedOnly && existing.LatestArchivePath != "" {
			if err := writeArchiveMetadata(ctx, db, sessionKey, existing.LatestArchivePath, now, "legacy_backfill_current_metadata", cfg.Retention.MinFreeBytes.Bytes); err != nil {
				metadataErrors = append(metadataErrors, err)
				result.ArtifactErrors++
			}
		}
	}
	if len(events) > 0 {
		if err := emitEvents(ctx, cfg, opts.HTTPClient, events); err != nil {
			return result, errors.Join(append(metadataErrors, err)...)
		}
		for _, event := range events {
			if _, err := db.ExecContext(ctx, `UPDATE sessions SET last_index_event_at=?,last_index_event_raw_sha256=?,last_index_event_metadata_sha256=? WHERE session_key=?`, nowText, event.rawSHA256, event.metadataSHA256, event.sessionKey); err != nil {
				return result, err
			}
		}
		result.EventsEmitted = len(events)
	}
	return result, errors.Join(metadataErrors...)
}

func mirrorChanged(file mirrorFile, existing existingSession, found bool) bool {
	return !found || existing.LatestRawSHA256 != file.RawSHA256
}

func scanMirror(cfg config.HubAppConfig) ([]mirrorFile, error) {
	var files []mirrorFile
	var problems []error
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
			if !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, localPath)
			if err != nil {
				return fmt.Errorf("relativize mirror file %s: %w", localPath, err)
			}
			parsed, err := parseMirrorPath(sender.SenderID, filepath.ToSlash(rel))
			if err != nil {
				if strings.HasPrefix(filepath.ToSlash(rel), ".asc/") {
					return nil
				}
				return fmt.Errorf("parse mirror file %s: %w", localPath, err)
			}
			if parsed.Role == "" {
				return nil
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
			parsed.PhysicalSHA256 = sum
			if parsed.Encoding == artifact.Plain {
				parsed.RawSHA256 = sum
				parsed.RawSizeBytes = info.Size()
			}
			files = append(files, parsed)
			return nil
		})
		if err != nil {
			problems = append(problems, err)
		}
	}
	return files, errors.Join(problems...)
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
	artifactPath := strings.Join(parts[2:], "/")
	role := artifact.Role(agent, artifactPath)
	if role == "" {
		return mirrorFile{}, nil
	}
	sessionID := ""
	var err error
	if role == artifact.Transcript {
		sessionID, err = sessionid.ForArtifact(agent, strings.TrimSuffix(artifactPath, ".zst"))
	}
	if err != nil {
		return mirrorFile{}, err
	}
	conversationID := ""
	if agent == constants.AgentCodex {
		if vendorID, vendorErr := sessionid.FromPath(agent, artifactPath); vendorErr == nil {
			conversationID = vendorID
		}
	}
	return mirrorFile{
		RelativePath: artifactPath, Role: role, Encoding: artifact.Encoding(artifactPath),
		SourceID:       sourcemetadata.ID(senderID, parts[0], agent),
		ArtifactID:     sourcemetadata.ArtifactID(sourcemetadata.ID(senderID, parts[0], agent), artifactPath),
		Sender:         senderID,
		User:           parts[0],
		Agent:          agent,
		SessionID:      sessionID,
		ConversationID: conversationID,
	}, nil
}

func loadExistingSession(ctx context.Context, db *sql.DB, sessionKey string) (existingSession, bool, error) {
	var existing existingSession
	err := db.QueryRowContext(ctx, `SELECT session_key, first_seen_at, last_index_event_at, last_changed_at, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256, archive_status, latest_archive_path, latest_archive_sha256,latest_physical_sha256
FROM sessions WHERE session_key = ?`, sessionKey).Scan(
		&existing.SessionKey,
		&existing.FirstSeenAt,
		&existing.LastIndexEventAt,
		&existing.LastChangedAt,
		&existing.LatestSizeBytes,
		&existing.LatestMtimeUnixNS,
		&existing.LatestRawSHA256,
		&existing.ArchiveStatus,
		&existing.LatestArchivePath,
		&existing.LatestArchiveSHA256,
		&existing.LatestPhysicalSHA256,
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
		(existing.LatestPhysicalSHA256 == file.PhysicalSHA256 || (existing.LatestPhysicalSHA256 == "" && existing.LatestSizeBytes == file.SizeBytes && existing.LatestMtimeUnixNS == file.MtimeUnixNS)) &&
		existing.LatestRawSHA256 == file.RawSHA256 {
		return existing.ArchiveStatus, existing.LatestArchivePath, existing.LatestArchiveSHA256
	}
	return constants.ArchiveStatusHot, "", ""
}

func upsertSession(ctx context.Context, db *sql.DB, file mirrorFile, sessionKey string, firstSeenAt string, lastIndexedAt string, lastChangedAt string, lastIndexEventAt string, archiveStatus string, archivePath string, archiveSHA256 string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO sessions (
session_key, agent, sender, user, env, host_name, session_id, conversation_id,
mirror_path, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256,
archive_status, latest_archive_path, latest_archive_sha256, first_seen_at,
last_indexed_at, last_changed_at, last_index_event_at
) VALUES (?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_key) DO UPDATE SET
	conversation_id = excluded.conversation_id,
	mirror_path = excluded.mirror_path,
	latest_size_bytes = excluded.latest_size_bytes,
	latest_mtime_unix_ns = excluded.latest_mtime_unix_ns,
	latest_raw_sha256 = excluded.latest_raw_sha256,
	archive_status = excluded.archive_status,
	latest_archive_path = excluded.latest_archive_path,
	latest_archive_sha256 = excluded.latest_archive_sha256,
	last_indexed_at = excluded.last_indexed_at,
	last_changed_at = excluded.last_changed_at,
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
		lastIndexedAt,
		lastChangedAt,
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

func buildEvent(file mirrorFile, runID, archiveStatus string, changed bool, changedAt string, observedAt time.Time) vlevent.AgentSessionIndex {
	parentSession, parentAgent := file.Native.ParentID, ""
	if file.Agent == constants.AgentClaude {
		parentSession = ""
		parentAgent = file.Native.ParentID
	}
	return vlevent.AgentSessionIndex{
		SchemaVersion: vlevent.IndexSchemaVersion,
		ArtifactID:    file.ArtifactID, SourceID: file.SourceID, ArtifactRole: file.Role,
		HostName: file.Descriptor.HostName, Env: file.Descriptor.Env,
		AgentID: file.Native.AgentID, RootSessionID: file.Native.RootID, ParentSessionID: parentSession, ParentAgentID: parentAgent, ForkedFromID: file.Native.ForkID,
		EventName:       "agent_session_index",
		ServiceName:     serviceName,
		Agent:           file.Agent,
		SenderID:        file.Sender,
		OSUser:          file.User,
		SessionID:       file.Native.SessionID,
		ConversationID:  file.ConversationID,
		FileSizeBytes:   file.SizeBytes,
		FileMtime:       time.Unix(0, file.MtimeUnixNS).UTC(),
		ArchiveStatus:   archiveStatus,
		SyncStatus:      constants.SyncStatusMirrored,
		CollectorRunID:  runID,
		FileChanged:     changed,
		FileChangedAt:   changedAt,
		IndexObservedAt: observedAt,
	}
}

func buildSessionKey(file mirrorFile) string {
	if file.LegacyKey != "" {
		return file.LegacyKey
	}
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
