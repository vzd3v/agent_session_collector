package readapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/vzd3v/agent_session_collector/sessionapi"
)

const (
	visible = "artifact_role='transcript' AND artifact_id<>''"
	fields  = "source_id,artifact_id,latest_raw_sha256,agent,native_session_id,native_root_id,native_agent_id,first_seen_at,last_changed_at,latest_raw_size_bytes"
	storage = `,relative_path,mirror_path,source_encoding,latest_physical_sha256,latest_size_bytes,latest_archive_path,
coalesce((SELECT a.archive_sha256 FROM session_archives AS a WHERE a.session_key=sessions.session_key
AND a.path=sessions.latest_archive_path AND a.archive_sha256=sessions.latest_archive_sha256
AND a.raw_sha256=sessions.latest_raw_sha256 AND a.raw_size_bytes=sessions.latest_raw_size_bytes LIMIT 1),'')`
	groupID = "CASE WHEN agent='claude' THEN coalesce(nullif(native_root_id,''),native_session_id) ELSE native_session_id END"
)

var (
	errMissing    = errors.New("session not found")
	errGeneration = errors.New("session generation unavailable")
	errLimit      = errors.New("session exceeds configured read limits")
)

type row struct {
	sessionapi.Session
	relativePath                            string
	mirrorPath, mirrorEncoding, physicalSHA string
	physicalSize                            int64
	archivePath, archiveSHA                 string
}

type scanRow interface{ Scan(...any) error }

func scan(scanner scanRow, withStorage bool) (row, error) {
	var p row
	fields := []any{&p.SourceID, &p.ArtifactID, &p.SHA256, &p.Provider, &p.NativeID, &p.RootID, &p.AgentID,
		&p.FirstSeenAt, &p.LastChangedAt, &p.SizeBytes}
	if withStorage {
		fields = append(fields, &p.relativePath, &p.mirrorPath, &p.mirrorEncoding, &p.physicalSHA, &p.physicalSize, &p.archivePath, &p.archiveSHA)
	}
	if err := scanner.Scan(fields...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return row{}, errMissing
		}
		return row{}, err
	}
	return p, nil
}

type listPosition struct {
	SourceID   string `json:"source_id"`
	ArtifactID string `json:"artifact_id"`
}

func (s *Server) listRows(ctx context.Context, q sessionapi.Query, after listPosition) (sessionapi.Page, error) {
	result := sessionapi.Page{Sessions: []sessionapi.Session{}}
	if len(q.SourceIDs) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(q.SourceIDs)+5)
	for _, id := range q.SourceIDs {
		args = append(args, id)
	}
	args = append(args, after.SourceID, after.ArtifactID)
	filter := ""
	if q.NativeID != "" {
		filter = " AND (native_session_id=? OR native_root_id=?)"
		args = append(args, q.NativeID, q.NativeID)
	}
	args = append(args, q.Limit+1)
	sqlText := "SELECT " + fields + " FROM sessions WHERE " + visible +
		" AND source_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(q.SourceIDs)), ",") + ")" +
		" AND (source_id,artifact_id)>(?,?)" + filter + " ORDER BY source_id,artifact_id LIMIT ?"
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		p, err := scan(rows, false)
		if err != nil {
			return result, err
		}
		result.Sessions = append(result.Sessions, p.Session)
	}
	return result, rows.Err()
}

func (s *Server) lookup(ctx context.Context, ref sessionapi.Ref) (row, error) {
	p, err := scan(s.db.QueryRowContext(ctx, "SELECT "+fields+storage+" FROM sessions WHERE "+
		visible+" AND source_id=? AND artifact_id=?", ref.SourceID, ref.ArtifactID), true)
	if err == nil && ref.SHA256 != "" && p.SHA256 != ref.SHA256 {
		return row{}, errGeneration
	}
	return p, err
}

func (s *Server) members(ctx context.Context, first row) ([]row, error) {
	id := first.NativeID
	project := ""
	if first.Provider == "claude" {
		if first.RootID != "" {
			id = first.RootID
		}
		project = claudeProject(first.relativePath, id)
	}
	if id == "" {
		return []row{first}, nil
	}
	const maxMembers = 64
	rows, err := s.db.QueryContext(ctx, "SELECT "+fields+storage+" FROM sessions WHERE "+visible+
		" AND source_id=? AND "+groupID+"=? ORDER BY artifact_id LIMIT ?",
		first.SourceID, id, maxMembers+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var members []row
	var anchor bool
	scanned := 0
	for rows.Next() {
		p, err := scan(rows, true)
		if err != nil {
			return nil, err
		}
		scanned++
		if first.Provider == "claude" && claudeProject(p.relativePath, id) != project {
			continue
		}
		members = append(members, p)
		if p.ArtifactID == first.ArtifactID && p.SHA256 == first.SHA256 {
			anchor = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if scanned > maxMembers {
		return nil, errLimit
	}
	if !anchor {
		return nil, errGeneration
	}
	return members, nil
}

func claudeProject(relativePath, nativeID string) string {
	project, _, child := strings.Cut(relativePath, "/"+nativeID+"/subagents/")
	if child {
		return project
	}
	return path.Dir(relativePath)
}

func (s *Server) ready(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations
WHERE target='hub' AND version='006_read_api.sql'`).Scan(&version); err != nil || version != 1 {
		return fmt.Errorf("read API requires a migrated hub catalog: %w", errors.Join(err, errNotReady))
	}
	return nil
}

var errNotReady = errors.New("read API schema unavailable")
