package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/checksum"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/schema"
)

type CompactOptions struct {
	Now func() time.Time
}

type CompactResult struct {
	Scanned       int
	Archived      int
	DeletedMirror int
}

type compactSession struct {
	SessionKey          string
	Agent               string
	Sender              string
	User                string
	SessionID           string
	MirrorPath          string
	LatestSizeBytes     int64
	LatestMtimeUnixNS   int64
	LatestRawSHA256     string
	LatestArchivePath   string
	LatestArchiveSHA256 string
}

func Compact(ctx context.Context, cfg config.HubAppConfig, opts CompactOptions) (CompactResult, error) {
	if !cfg.Archive.Enabled {
		return CompactResult{}, nil
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	if err := os.MkdirAll(cfg.Paths.Archive, 0o750); err != nil {
		return CompactResult{}, fmt.Errorf("create archive root: %w", err)
	}
	db, err := schema.Open(cfg.Paths.Catalog)
	if err != nil {
		return CompactResult{}, err
	}
	defer func() {
		_ = db.Close()
	}()
	if err := schema.Apply(ctx, db, schema.Hub); err != nil {
		return CompactResult{}, err
	}
	sessions, err := loadHotSessions(ctx, db)
	if err != nil {
		return CompactResult{}, err
	}
	result := CompactResult{Scanned: len(sessions)}
	cutoff := now.Add(-cfg.Archive.QuietAfter.Duration)
	for _, session := range sessions {
		mtime := time.Unix(0, session.LatestMtimeUnixNS).UTC()
		if mtime.After(cutoff) {
			continue
		}
		archived, err := archiveSession(ctx, db, cfg, session, now)
		if err != nil {
			return result, err
		}
		if !archived {
			continue
		}
		result.Archived++
		if cfg.Archive.Policy == "versioned" {
			if err := pruneVersionedArchives(ctx, db, cfg, session.SessionKey, now); err != nil {
				return result, err
			}
		}
	}
	deleted, err := deleteRetainedArchivedMirrors(ctx, db, cfg, now)
	if err != nil {
		return result, err
	}
	result.DeletedMirror += deleted
	return result, nil
}

func loadHotSessions(ctx context.Context, db *sql.DB) ([]compactSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent, sender, user, session_id, mirror_path, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256, latest_archive_path, latest_archive_sha256
FROM sessions WHERE archive_status = ? ORDER BY session_key`, constants.ArchiveStatusHot)
	if err != nil {
		return nil, fmt.Errorf("query hot sessions: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var sessions []compactSession
	for rows.Next() {
		var session compactSession
		if err := rows.Scan(
			&session.SessionKey,
			&session.Agent,
			&session.Sender,
			&session.User,
			&session.SessionID,
			&session.MirrorPath,
			&session.LatestSizeBytes,
			&session.LatestMtimeUnixNS,
			&session.LatestRawSHA256,
			&session.LatestArchivePath,
			&session.LatestArchiveSHA256,
		); err != nil {
			return nil, fmt.Errorf("scan hot session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hot sessions: %w", err)
	}
	return sessions, nil
}

func archiveSession(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, session compactSession, now time.Time) (bool, error) {
	info, err := os.Stat(session.MirrorPath)
	if err != nil {
		return false, fmt.Errorf("stat mirror before archive %s: %w", session.MirrorPath, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("mirror path is not a regular file: %s", session.MirrorPath)
	}
	if info.Size() != session.LatestSizeBytes || info.ModTime().UnixNano() != session.LatestMtimeUnixNS {
		return false, fmt.Errorf("mirror file changed since index: %s", session.MirrorPath)
	}
	rawSHA256, err := checksum.FileSHA256(session.MirrorPath)
	if err != nil {
		return false, err
	}
	if rawSHA256 != session.LatestRawSHA256 {
		return false, fmt.Errorf("mirror checksum mismatch for %s", session.MirrorPath)
	}
	version, err := nextArchiveVersion(ctx, db, cfg.Archive.Policy, session.SessionKey)
	if err != nil {
		return false, err
	}
	finalPath := archivePath(cfg, session, version)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return false, fmt.Errorf("create archive dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(finalPath), ".asc-archive-*.zst")
	if err != nil {
		return false, fmt.Errorf("create archive temp: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("close archive temp: %w", err)
	}
	if err := compressZstd(ctx, cfg, session.MirrorPath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if err := verifyZstd(ctx, tmpPath, session.LatestRawSHA256); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	archiveSHA256, err := checksum.FileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if _, err := os.Stat(finalPath); err == nil {
		_ = os.Remove(tmpPath)
		if err := verifyZstd(ctx, finalPath, session.LatestRawSHA256); err != nil {
			return false, err
		}
		archiveSHA256, err = checksum.FileSHA256(finalPath)
		if err != nil {
			return false, err
		}
	} else if os.IsNotExist(err) {
		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return false, fmt.Errorf("commit archive: %w", err)
		}
	} else {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("stat final archive: %w", err)
	}
	if err := recordArchive(ctx, db, cfg, session, version, finalPath, archiveSHA256, now); err != nil {
		return false, err
	}
	return true, nil
}

func nextArchiveVersion(ctx context.Context, db *sql.DB, policy string, sessionKey string) (int, error) {
	if policy != "versioned" {
		return 0, nil
	}
	var maxVersion sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM session_archives WHERE session_key = ?`, sessionKey).Scan(&maxVersion); err != nil {
		return 0, fmt.Errorf("query archive version: %w", err)
	}
	if !maxVersion.Valid {
		return 1, nil
	}
	return int(maxVersion.Int64) + 1, nil
}

func archivePath(cfg config.HubAppConfig, session compactSession, version int) string {
	baseName := safeArchiveFileName(session.SessionID)
	if baseName == "" {
		baseName = hashShort(session.SessionKey)
	}
	stableName := baseName + "." + hashShort(session.SessionKey) + "." + hashShort(session.LatestRawSHA256)
	switch cfg.Archive.Policy {
	case "versioned":
		baseName = fmt.Sprintf("%s.v%d.jsonl.zst", stableName, version)
	default:
		baseName = stableName + ".jsonl.zst"
	}
	return filepath.Join(cfg.Paths.Archive, safeArchiveFileName(session.Sender), safeArchiveFileName(session.User), safeArchiveFileName(session.Agent), baseName)
}

func safeArchiveFileName(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), ".")
}

func hashShort(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func compressZstd(ctx context.Context, cfg config.HubAppConfig, inputPath string, outputPath string) error {
	level := "-" + strconv.Itoa(cfg.Archive.ZstdLevel)
	cmd := exec.CommandContext(ctx, "zstd", "-q", level, "-f", "--no-progress", "-o", outputPath, inputPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run zstd: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyZstd(ctx context.Context, path string, expectedRawSHA256 string) error {
	cmd := exec.CommandContext(ctx, "zstd", "-q", "-dc", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("prepare zstd verification: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zstd verification: %w", err)
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return fmt.Errorf("read decompressed zstd archive: %w", copyErr)
	}
	if waitErr != nil {
		return fmt.Errorf("verify zstd archive: %w", waitErr)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedRawSHA256 {
		return fmt.Errorf("archive raw checksum mismatch: got %s want %s", got, expectedRawSHA256)
	}
	return nil
}

func recordArchive(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, session compactSession, version int, archivePath string, archiveSHA256 string, now time.Time) error {
	nowText := now.Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin archive catalog update: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if cfg.Archive.Policy == "versioned" {
		if _, err := tx.ExecContext(ctx, `UPDATE session_archives SET superseded_at = ? WHERE session_key = ? AND superseded_at = ''`, nowText, session.SessionKey); err != nil {
			return fmt.Errorf("supersede archive versions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_archives(session_key, version, path, raw_size_bytes, raw_mtime_unix_ns, raw_sha256, archive_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, session.SessionKey, version, archivePath, session.LatestSizeBytes, session.LatestMtimeUnixNS, session.LatestRawSHA256, archiveSHA256, nowText); err != nil {
			return fmt.Errorf("insert archive version: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_archives(session_key, version, path, raw_size_bytes, raw_mtime_unix_ns, raw_sha256, archive_sha256, created_at)
VALUES (?, 0, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_key, version) DO UPDATE SET
path = excluded.path,
raw_size_bytes = excluded.raw_size_bytes,
raw_mtime_unix_ns = excluded.raw_mtime_unix_ns,
raw_sha256 = excluded.raw_sha256,
archive_sha256 = excluded.archive_sha256,
created_at = excluded.created_at,
superseded_at = ''`, session.SessionKey, archivePath, session.LatestSizeBytes, session.LatestMtimeUnixNS, session.LatestRawSHA256, archiveSHA256, nowText); err != nil {
			return fmt.Errorf("upsert replace archive: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET archive_status = ?, latest_archive_path = ?, latest_archive_sha256 = ? WHERE session_key = ?`, constants.ArchiveStatusArchived, archivePath, archiveSHA256, session.SessionKey); err != nil {
		return fmt.Errorf("update session archive status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit archive catalog update: %w", err)
	}
	return nil
}

func deleteMirrorAfterRetainWindow(cfg config.HubAppConfig, session compactSession, archiveCreatedAt time.Time, now time.Time) (bool, error) {
	if now.Sub(archiveCreatedAt) < cfg.Archive.RetainPlainAfterArchive.Duration {
		return false, nil
	}
	matches, err := mirrorMatchesCatalog(session)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, nil
	}
	trashPath, moved, err := moveMirrorForDeletion(session.MirrorPath)
	if err != nil || !moved {
		return false, err
	}
	matches, err = mirrorPathMatchesCatalog(session, trashPath)
	if err != nil {
		return false, errors.Join(err, restoreMovedMirror(trashPath, session.MirrorPath))
	}
	if !matches {
		if err := restoreMovedMirror(trashPath, session.MirrorPath); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := os.Remove(trashPath); err != nil {
		return false, fmt.Errorf("delete archived mirror file: %w", err)
	}
	return true, nil
}

func mirrorMatchesCatalog(session compactSession) (bool, error) {
	return mirrorPathMatchesCatalog(session, session.MirrorPath)
}

func mirrorPathMatchesCatalog(session compactSession, candidatePath string) (bool, error) {
	info, err := os.Stat(candidatePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat mirror before retained deletion: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if info.Size() != session.LatestSizeBytes || info.ModTime().UnixNano() != session.LatestMtimeUnixNS {
		return false, nil
	}
	rawSHA256, err := checksum.FileSHA256(candidatePath)
	if err != nil {
		return false, err
	}
	return rawSHA256 == session.LatestRawSHA256, nil
}

func moveMirrorForDeletion(mirrorPath string) (string, bool, error) {
	dir := filepath.Dir(mirrorPath)
	base := filepath.Base(mirrorPath)
	trashFile, err := os.CreateTemp(dir, "."+base+".delete-*")
	if err != nil {
		return "", false, fmt.Errorf("create mirror delete temp: %w", err)
	}
	trashPath := trashFile.Name()
	if err := trashFile.Close(); err != nil {
		_ = os.Remove(trashPath)
		return "", false, fmt.Errorf("close mirror delete temp: %w", err)
	}
	if err := os.Remove(trashPath); err != nil {
		return "", false, fmt.Errorf("remove mirror delete placeholder: %w", err)
	}
	if err := os.Rename(mirrorPath, trashPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("move mirror for retained deletion: %w", err)
	}
	return trashPath, true, nil
}

func restoreMovedMirror(trashPath string, mirrorPath string) error {
	if err := os.Link(trashPath, mirrorPath); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("mirror changed during retained deletion; preserved moved file at %s", trashPath)
		}
		return fmt.Errorf("restore retained mirror after verification mismatch: %w", err)
	}
	if err := os.Remove(trashPath); err != nil {
		return fmt.Errorf("remove restored mirror quarantine: %w", err)
	}
	return nil
}

type archiveRecord struct {
	Version   int
	Path      string
	CreatedAt time.Time
}

func pruneVersionedArchives(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, sessionKey string, now time.Time) error {
	if cfg.Archive.VersionRetention == 0 && cfg.Archive.VersionRetentionDays == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version, path, created_at FROM session_archives WHERE session_key = ? ORDER BY version DESC`, sessionKey)
	if err != nil {
		return fmt.Errorf("query archive versions for prune: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var records []archiveRecord
	for rows.Next() {
		var record archiveRecord
		var createdAt string
		if err := rows.Scan(&record.Version, &record.Path, &createdAt); err != nil {
			return fmt.Errorf("scan archive version: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return fmt.Errorf("parse archive created_at: %w", err)
		}
		record.CreatedAt = parsed
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archive versions: %w", err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Version > records[j].Version })
	keep := map[int]struct{}{}
	for i, record := range records {
		if cfg.Archive.VersionRetention > 0 && i < cfg.Archive.VersionRetention {
			keep[record.Version] = struct{}{}
			continue
		}
		if cfg.Archive.VersionRetentionDays > 0 && now.Sub(record.CreatedAt) < time.Duration(cfg.Archive.VersionRetentionDays)*24*time.Hour {
			keep[record.Version] = struct{}{}
		}
	}
	for _, record := range records {
		if _, ok := keep[record.Version]; ok {
			continue
		}
		if err := os.Remove(record.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove pruned archive: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM session_archives WHERE session_key = ? AND version = ?`, sessionKey, record.Version); err != nil {
			return fmt.Errorf("delete pruned archive row: %w", err)
		}
	}
	return nil
}

func deleteRetainedArchivedMirrors(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) (int, error) {
	if !cfg.Retention.DeleteMirrorAfterArchive {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT s.session_key, s.agent, s.sender, s.user, s.session_id, s.mirror_path, s.latest_size_bytes, s.latest_mtime_unix_ns, s.latest_raw_sha256, s.latest_archive_path, s.latest_archive_sha256, a.created_at
FROM sessions s
JOIN session_archives a ON a.session_key = s.session_key AND a.path = s.latest_archive_path
WHERE s.archive_status = ?`, constants.ArchiveStatusArchived)
	if err != nil {
		return 0, fmt.Errorf("query archived mirrors for deletion: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	deleted := 0
	for rows.Next() {
		var session compactSession
		var createdAtText string
		if err := rows.Scan(
			&session.SessionKey,
			&session.Agent,
			&session.Sender,
			&session.User,
			&session.SessionID,
			&session.MirrorPath,
			&session.LatestSizeBytes,
			&session.LatestMtimeUnixNS,
			&session.LatestRawSHA256,
			&session.LatestArchivePath,
			&session.LatestArchiveSHA256,
			&createdAtText,
		); err != nil {
			return deleted, fmt.Errorf("scan archived mirror for deletion: %w", err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, createdAtText)
		if err != nil {
			return deleted, fmt.Errorf("parse archive created_at: %w", err)
		}
		ok, err := deleteMirrorAfterRetainWindow(cfg, session, createdAt, now)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	if err := rows.Err(); err != nil {
		return deleted, fmt.Errorf("iterate archived mirrors for deletion: %w", err)
	}
	return deleted, nil
}
