package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/checksum"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/diskspace"
	"github.com/vzd3v/agent_session_collector/internal/schema"
)

type CompactOptions struct {
	Now    func() time.Time
	DryRun bool
}

type CompactResult struct {
	Scanned       int
	Archived      int
	DeletedMirror int
	Plan          *RetentionPlan
}

type compactSession struct {
	SourceEncoding       string
	LatestPhysicalSHA256 string
	LatestRawSizeBytes   int64
	SessionKey           string
	Agent                string
	Sender               string
	User                 string
	SessionID            string
	MirrorPath           string
	LatestSizeBytes      int64
	LatestMtimeUnixNS    int64
	LatestRawSHA256      string
	LatestArchivePath    string
	LatestArchiveSHA256  string
	Role                 string
}

func Compact(ctx context.Context, cfg config.HubAppConfig, opts CompactOptions) (CompactResult, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	if opts.DryRun {
		db, err := schema.OpenReadOnly(cfg.Paths.Catalog)
		if err != nil {
			return CompactResult{}, err
		}
		defer func() { _ = db.Close() }()
		if err := requireRetentionSchema(ctx, db); err != nil {
			return CompactResult{}, err
		}
		// One read transaction gives the preview a consistent catalog snapshot;
		// filesystem generations are independently checked and may still advance.
		if _, err := db.ExecContext(ctx, `BEGIN`); err != nil {
			return CompactResult{}, err
		}
		defer func() { _, _ = db.ExecContext(context.Background(), `ROLLBACK`) }()
		plan, err := buildRetentionPlan(ctx, db, cfg, now)
		if err != nil {
			return CompactResult{}, err
		}
		result := CompactResult{Plan: &plan}
		if plan.PressureUnsatisfied {
			return result, fmt.Errorf("pressure_unsatisfied: protected data prevents retention targets")
		}
		return result, nil
	}
	unlock, err := lockHub(ctx, cfg.Paths.Catalog)
	if err != nil {
		return CompactResult{}, err
	}
	defer unlock()
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
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		return CompactResult{}, err
	}
	if err := recoverExpiryGroups(ctx, db, cfg); err != nil {
		return CompactResult{}, err
	}
	if err := repairCurrentArchiveMetadata(ctx, db, cfg, now); err != nil {
		return CompactResult{}, err
	}
	if _, err := drainDeletions(ctx, db, cfg, now); err != nil {
		return CompactResult{}, err
	}
	before, err := buildRetentionPlan(ctx, db, cfg, now)
	if err != nil {
		return CompactResult{}, err
	}
	if err := applyRetentionPlan(ctx, db, cfg, before, now); err != nil {
		return CompactResult{}, err
	}
	if _, err := drainDeletions(ctx, db, cfg, now); err != nil {
		return CompactResult{}, err
	}
	sessions, err := loadHotSessions(ctx, db)
	if err != nil {
		return CompactResult{}, err
	}
	result := CompactResult{Scanned: len(sessions)}
	cutoff := now.Add(-cfg.Archive.QuietAfter.Duration)
	for _, session := range sessions {
		if !cfg.Archive.Enabled {
			break
		}
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
	}
	deleted, err := deleteVerifiedMirrors(ctx, db, cfg, now)
	if err != nil {
		return result, err
	}
	result.DeletedMirror += deleted
	plan, err := buildRetentionPlan(ctx, db, cfg, now)
	if err != nil {
		return result, err
	}
	if err := applyRetentionPlan(ctx, db, cfg, plan, now); err != nil {
		return result, err
	}
	if _, err := drainDeletions(ctx, db, cfg, now); err != nil {
		return result, err
	}
	final, err := buildRetentionPlan(ctx, db, cfg, now)
	if err != nil {
		return result, err
	}
	actual := final
	actual.PlannedBytes = 0
	actual.PlannedMirrorBytes = 0
	final.PressureUnsatisfied = retentionPressure(cfg, actual)
	result.Plan = &final
	if final.PressureUnsatisfied {
		return result, fmt.Errorf("pressure_unsatisfied: protected data prevents retention targets")
	}
	return result, nil
}

func loadHotSessions(ctx context.Context, db *sql.DB) ([]compactSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent, sender, user, session_id, mirror_path, latest_size_bytes, latest_mtime_unix_ns, latest_raw_sha256, latest_archive_path, latest_archive_sha256,source_encoding,latest_physical_sha256,latest_raw_size_bytes,artifact_role
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
			&session.SourceEncoding, &session.LatestPhysicalSHA256, &session.LatestRawSizeBytes,
			&session.Role,
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
	if rawSHA256 != physicalHash(session) {
		return false, fmt.Errorf("mirror checksum mismatch for %s", session.MirrorPath)
	}
	expected := session.LatestSizeBytes
	if session.SourceEncoding != artifact.Zstd {
		expected += expected/128 + 1024*1024
	}
	if err := diskspace.Check(cfg.Paths.Archive, cfg.Retention.MinFreeBytes.Bytes, expected); err != nil {
		return false, fmt.Errorf("archive admission: %w", err)
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
	if err := stageArchive(ctx, cfg, session, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if err := verifyZstd(ctx, tmpPath, session.LatestRawSHA256, logicalSize(session)); err != nil {
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
		if err := verifyZstd(ctx, finalPath, session.LatestRawSHA256, logicalSize(session)); err != nil {
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
	committed, err := os.Open(finalPath)
	if err != nil {
		return false, err
	}
	syncErr := committed.Sync()
	closeErr := committed.Close()
	if syncErr != nil || closeErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	if err := syncStorageParents(cfg.Paths.Archive, filepath.Dir(finalPath)); err != nil {
		return false, err
	}
	if err := writeArchiveMetadata(ctx, db, session.SessionKey, finalPath, now, "archive_time", cfg.Retention.MinFreeBytes.Bytes); err != nil {
		return false, err
	}
	if err := syncArchiveSidecar(finalPath); err != nil {
		return false, err
	}
	if err := recordArchive(ctx, db, cfg, session, version, finalPath, archiveSHA256, now); err != nil {
		return false, err
	}
	return true, nil
}

func physicalHash(session compactSession) string {
	if session.LatestPhysicalSHA256 != "" {
		return session.LatestPhysicalSHA256
	}
	return session.LatestRawSHA256
}
func logicalSize(session compactSession) int64 {
	if session.SourceEncoding == artifact.Zstd {
		return session.LatestRawSizeBytes
	}
	return session.LatestSizeBytes
}

func stageArchive(ctx context.Context, cfg config.HubAppConfig, session compactSession, outputPath string) error {
	if session.SourceEncoding != artifact.Zstd {
		return compressZstd(ctx, cfg, session.MirrorPath, outputPath)
	}
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	copyErr := artifact.Read(ctx, session.MirrorPath, artifact.Plain, func(r io.Reader) error { _, err := io.Copy(output, r); return err })
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	sum, err := checksum.FileSHA256(outputPath)
	if err != nil {
		return err
	}
	if sum != physicalHash(session) {
		return fmt.Errorf("native compressed input changed during archive")
	}
	return nil
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
	extension := "jsonl"
	switch session.Role {
	case artifact.SubagentMetadata:
		extension = "json"
	case artifact.ToolResult:
		extension = "bin"
	}
	if session.SourceEncoding == artifact.Zstd {
		stableName += "." + hashShort(physicalHash(session))
	}
	switch cfg.Archive.Policy {
	case "versioned":
		baseName = fmt.Sprintf("%s.v%d.%s.zst", stableName, version, extension)
	default:
		baseName = stableName + "." + extension + ".zst"
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

func verifyZstd(ctx context.Context, name string, expectedRawSHA256 string, expectedSize ...int64) error {
	h := sha256.New()
	err := artifact.Read(ctx, name, artifact.Zstd, func(r io.Reader) error {
		var n int64
		var err error
		if len(expectedSize) > 0 {
			n, err = io.Copy(h, io.LimitReader(r, expectedSize[0]+1))
			if err == nil && n != expectedSize[0] {
				return fmt.Errorf("archive logical size mismatch")
			}
		} else {
			_, err = io.Copy(h, r)
		}
		return err
	})
	if err != nil {
		return err
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
	if cfg.Archive.Policy != "versioned" {
		var oldPath, oldHash string
		err := tx.QueryRowContext(ctx, `SELECT path,archive_sha256 FROM session_archives WHERE session_key=? AND version=0`, session.SessionKey).Scan(&oldPath, &oldHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && oldPath != archivePath {
			proof := &archiveProof{Path: archivePath, Hash: archiveSHA256, RawHash: session.LatestRawSHA256, RawSize: logicalSize(session)}
			if err := queueBackedFile(ctx, tx, cfg, "archive", oldPath, oldHash, "replaced", proof, now); err != nil {
				return err
			}
			oldSidecarHash, _, sideErr := physicalFile(ctx, oldPath+".metadata.json")
			if sideErr != nil && !errors.Is(sideErr, os.ErrNotExist) {
				return sideErr
			}
			if sideErr == nil {
				if err := queueBackedFile(ctx, tx, cfg, "archive", oldPath+".metadata.json", oldSidecarHash, "replaced", proof, now); err != nil {
					return err
				}
			}
		}
	}
	if cfg.Archive.Policy == "versioned" {
		if _, err := tx.ExecContext(ctx, `UPDATE session_archives SET superseded_at = ? WHERE session_key = ? AND superseded_at = ''`, nowText, session.SessionKey); err != nil {
			return fmt.Errorf("supersede archive versions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_archives(session_key, version, path, raw_size_bytes, raw_mtime_unix_ns, raw_sha256, archive_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, session.SessionKey, version, archivePath, logicalSize(session), session.LatestMtimeUnixNS, session.LatestRawSHA256, archiveSHA256, nowText); err != nil {
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
superseded_at = ''`, session.SessionKey, archivePath, logicalSize(session), session.LatestMtimeUnixNS, session.LatestRawSHA256, archiveSHA256, nowText); err != nil {
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

func mirrorMatchesCatalog(session compactSession) (bool, error) {
	info, err := os.Lstat(session.MirrorPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != session.LatestSizeBytes || info.ModTime().UnixNano() != session.LatestMtimeUnixNS {
		return false, nil
	}
	hash, _, err := physicalFile(context.Background(), session.MirrorPath)
	return hash == physicalHash(session), err
}

// A valid catalog pointer alone never authorizes removal of the last plain copy.
func deleteVerifiedMirrors(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) (int, error) {
	if !cfg.Retention.DeleteMirrorAfterArchive {
		return 0, nil
	}
	sessions, err := loadRetentionSessions(ctx, db)
	if err != nil {
		return 0, err
	}
	queued := 0
	var candidates []string
	for _, s := range sessions {
		if s.Status != constants.ArchiveStatusArchived {
			continue
		}
		var created time.Time
		for _, a := range s.archives {
			if a.Path == s.LatestArchivePath {
				created, _ = validClock(a.Created)
				break
			}
		}
		if created.IsZero() || now.Sub(created) < cfg.Archive.RetainPlainAfterArchive.Duration {
			continue
		}
		matches, err := mirrorMatchesCatalog(s.compactSession)
		if err != nil {
			return queued, err
		}
		if !matches {
			continue
		}
		if err := verifyCurrentArchive(ctx, s.compactSession); err != nil {
			return queued, fmt.Errorf("preserve plain copy: %w", err)
		}
		// Sidecars are part of the portable archive. Repair a missing legacy snapshot
		// before plain deletion; existing snapshots are never rewritten.
		if err := writeArchiveMetadata(ctx, db, s.SessionKey, s.LatestArchivePath, now, "legacy_backfill_current_metadata", cfg.Retention.MinFreeBytes.Bytes); err != nil {
			return queued, err
		}
		if err := syncArchiveSidecar(s.LatestArchivePath); err != nil {
			return queued, err
		}
		var metadata struct {
			RawSHA      string `json:"raw_sha256"`
			PhysicalSHA string `json:"physical_sha256"`
			ArtifactID  string `json:"artifact_id"`
		}
		err = artifact.Read(ctx, s.LatestArchivePath+".metadata.json", artifact.Plain, func(r io.Reader) error { return json.NewDecoder(io.LimitReader(r, 128*1024)).Decode(&metadata) })
		if err != nil || metadata.RawSHA != s.LatestRawSHA256 || metadata.PhysicalSHA != physicalHash(s.compactSession) || (s.ArtifactID != "" && metadata.ArtifactID != s.ArtifactID) {
			return queued, fmt.Errorf("preserve plain copy: invalid archive metadata snapshot")
		}
		if err := queueBackedFile(ctx, db, cfg, "mirror", s.MirrorPath, physicalHash(s.compactSession), "archived_plain", proofFor(s), now); err != nil {
			return queued, err
		}
		queued++
		candidates = append(candidates, s.MirrorPath)
	}
	if _, err := drainDeletions(ctx, db, cfg, now); err != nil {
		return queued, err
	}
	deleted := 0
	for _, name := range candidates {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			deleted++
		}
	}
	return deleted, nil
}

func syncArchiveSidecar(archive string) error {
	f, err := os.Open(archive + ".metadata.json")
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	return syncDirectory(filepath.Dir(archive))
}
