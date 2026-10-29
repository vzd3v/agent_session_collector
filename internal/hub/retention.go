package hub

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/constants"
	"github.com/vzd3v/agent_session_collector/internal/diskspace"
)

type RetentionEntry struct {
	Group       string   `json:"group"`
	Reason      string   `json:"reason"`
	Bytes       int64    `json:"bytes"`
	MirrorBytes int64    `json:"mirror_bytes,omitempty"`
	Sessions    []string `json:"sessions,omitempty"`
}
type RetentionPlan struct {
	ArchiveBytes         int64            `json:"archive_bytes"`
	PlannedBytes         int64            `json:"planned_archive_bytes"`
	AvailableBytes       int64            `json:"available_bytes"`
	MirrorAvailableBytes int64            `json:"mirror_available_bytes"`
	PlannedMirrorBytes   int64            `json:"planned_mirror_bytes"`
	SharedFilesystem     bool             `json:"shared_filesystem"`
	PressureUnsatisfied  bool             `json:"pressure_unsatisfied"`
	Actions              []RetentionEntry `json:"actions"`
	Protected            []RetentionEntry `json:"protected"`
	expire               [][]retentionSession
	retire               []retentionArchive
	orphan               []deletion
}
type retentionSession struct {
	compactSession
	SourceID, ArtifactID, RelativePath, Role, NativeID, NativeRoot, NativeParent, LastChanged, FirstSeen, Status string
	archives                                                                                                     []retentionArchive
	anchor                                                                                                       time.Time
}
type retentionArchive struct {
	Key, Path, Hash, RawHash, Created string
	Version                           int
	Bytes                             int64
	Backing                           *archiveProof
}
type retentionGroup struct {
	key         string
	sessions    []retentionSession
	anchor      time.Time
	bytes       int64
	mirrorBytes int64
	protected   string
}

func loadRetentionSessions(ctx context.Context, db *sql.DB) ([]retentionSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key,source_id,artifact_id,relative_path,artifact_role,agent,sender,user,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,latest_raw_sha256,latest_archive_path,latest_archive_sha256,source_encoding,latest_physical_sha256,latest_raw_size_bytes,native_session_id,native_root_id,native_parent_id,last_changed_at,first_seen_at,archive_status FROM sessions ORDER BY session_key`)
	if err != nil {
		return nil, err
	}
	var out []retentionSession
	for rows.Next() {
		var s retentionSession
		if err := rows.Scan(&s.SessionKey, &s.SourceID, &s.ArtifactID, &s.RelativePath, &s.Role, &s.Agent, &s.Sender, &s.User, &s.SessionID, &s.MirrorPath, &s.LatestSizeBytes, &s.LatestMtimeUnixNS, &s.LatestRawSHA256, &s.LatestArchivePath, &s.LatestArchiveSHA256, &s.SourceEncoding, &s.LatestPhysicalSHA256, &s.LatestRawSizeBytes, &s.NativeID, &s.NativeRoot, &s.NativeParent, &s.LastChanged, &s.FirstSeen, &s.Status); err != nil {
			_ = rows.Close()
			return nil, err
		}
		s.anchor, _ = validClock(s.LastChanged)
		if s.anchor.IsZero() {
			s.anchor, _ = validClock(s.FirstSeen)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT session_key,version,path,archive_sha256,raw_sha256,created_at FROM session_archives ORDER BY session_key,version DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	positions := map[string]int{}
	for i, s := range out {
		positions[s.SessionKey] = i
	}
	for rows.Next() {
		var a retentionArchive
		if err := rows.Scan(&a.Key, &a.Version, &a.Path, &a.Hash, &a.RawHash, &a.Created); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(a.Path); err == nil && info.Mode().IsRegular() {
			a.Bytes = info.Size()
		}
		if i, ok := positions[a.Key]; ok {
			out[i].archives = append(out[i].archives, a)
		}
	}
	return out, rows.Err()
}
func validClock(text string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || t.IsZero() {
		return time.Time{}, fmt.Errorf("unknown clock")
	}
	return t.UTC(), nil
}
func sessionSource(s retentionSession) string {
	if s.SourceID != "" {
		return s.SourceID
	}
	return s.Sender + "\x00" + s.User + "\x00" + s.Agent
}

// Only explicit vendor identity edges join sessions. Fork ancestry is deliberately
// excluded. Union/find is bounded even if malformed parent graphs contain cycles.
func groupSessions(sessions []retentionSession) []retentionGroup {
	parent := make([]int, len(sessions))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	join := func(i, j int) {
		a, b := find(i), find(j)
		if a != b {
			parent[b] = a
		}
	}
	identities := map[string]int{}
	roots := map[string]int{}
	paths := map[string]int{}
	for i, s := range sessions {
		source := sessionSource(s)
		if s.NativeID != "" {
			key := source + "\x00" + s.NativeID
			if j, ok := identities[key]; ok {
				join(i, j)
			} else {
				identities[key] = i
			}
		}
		if s.NativeRoot != "" {
			key := source + "\x00" + s.NativeRoot
			if j, ok := roots[key]; ok {
				join(i, j)
			} else {
				roots[key] = i
			}
		}
		if s.Role == artifact.Transcript {
			paths[source+"\x00"+strings.TrimSuffix(strings.TrimSuffix(s.RelativePath, ".zst"), ".jsonl")] = i
		}
	}
	knownTool := map[int]bool{}
	for i, s := range sessions {
		source := sessionSource(s)
		if s.Agent == constants.AgentCodex && s.NativeParent != "" {
			if j, ok := identities[source+"\x00"+s.NativeParent]; ok {
				join(i, j)
			}
		}
		if s.Agent == constants.AgentClaude && s.Role == artifact.ToolResult {
			marker := "/tool-results/"
			at := strings.LastIndex(s.RelativePath, marker)
			if at >= 0 {
				stem := s.RelativePath[:at]
				if j, ok := paths[source+"\x00"+stem]; ok {
					root := sessions[j]
					id := path.Base(stem)
					if root.NativeRoot == id && id != "" {
						join(i, j)
						knownTool[i] = true
					}
				}
			}
		}
	}
	groups := map[int]*retentionGroup{}
	for i, s := range sessions {
		n := find(i)
		g := groups[n]
		if g == nil {
			g = &retentionGroup{key: s.SessionKey}
			groups[n] = g
		}
		g.sessions = append(g.sessions, s)
		if s.SessionKey < g.key {
			g.key = s.SessionKey
		}
		if s.anchor.After(g.anchor) {
			g.anchor = s.anchor
		}
		if s.Role == artifact.ToolResult && !knownTool[i] {
			g.protected = "unknown_companion_owner"
		}
		if s.Role == artifact.SessionNameIndex {
			g.protected = "source_global_index"
		}
	}
	out := make([]retentionGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].anchor.Equal(out[j].anchor) {
			return out[i].key < out[j].key
		}
		return out[i].anchor.Before(out[j].anchor)
	})
	return out
}

func verifyCurrentArchive(ctx context.Context, s compactSession) error {
	if s.LatestArchivePath == "" || len(s.LatestArchiveSHA256) != 64 {
		return fmt.Errorf("missing_current_archive")
	}
	hash, _, err := physicalFile(ctx, s.LatestArchivePath)
	if err != nil {
		return err
	}
	if hash != s.LatestArchiveSHA256 {
		return fmt.Errorf("archive_checksum_mismatch")
	}
	return verifyZstd(ctx, s.LatestArchivePath, s.LatestRawSHA256, logicalSize(s))
}

func groupProtection(cfg config.HubAppConfig, g *retentionGroup, now time.Time) string {
	if g.protected != "" {
		return g.protected
	}
	for _, s := range g.sessions {
		if s.SourceID == "" || s.ArtifactID == "" || s.RelativePath == "" {
			return "needs_index"
		}
		if s.anchor.IsZero() || s.anchor.After(now) {
			return "unknown_content_clock"
		}
		if now.Sub(s.anchor) < cfg.Archive.QuietAfter.Duration {
			return "active_content"
		}
		if s.Status != constants.ArchiveStatusArchived {
			return "unarchived"
		}
		found := false
		for _, a := range s.archives {
			if a.Path == s.LatestArchivePath {
				found = true
				created, err := validClock(a.Created)
				if err != nil {
					return "unknown_archive_clock"
				}
				if now.Sub(created) < cfg.Archive.RetainPlainAfterArchive.Duration {
					return "plain_grace"
				}
			}
		}
		if !found {
			return "missing_archive_record"
		}
		if info, err := os.Lstat(s.MirrorPath); err == nil {
			if !info.Mode().IsRegular() || info.Size() != s.LatestSizeBytes || info.ModTime().UnixNano() != s.LatestMtimeUnixNS || time.Unix(0, s.LatestMtimeUnixNS).After(now.Add(-cfg.Archive.QuietAfter.Duration)) {
				return "active_mirror"
			}
		} else if !os.IsNotExist(err) {
			return "unreadable_mirror"
		}
	}
	return ""
}

func eligibleGroup(ctx context.Context, cfg config.HubAppConfig, g *retentionGroup, now time.Time) string {
	if reason := groupProtection(cfg, g, now); reason != "" {
		return reason
	}
	for _, s := range g.sessions {
		if err := verifyCurrentArchive(ctx, s.compactSession); err != nil {
			return "unverified_archive"
		}
		if _, err := os.Lstat(s.MirrorPath); err == nil {
			matches, err := mirrorMatchesCatalog(s.compactSession)
			if err != nil || !matches {
				return "active_mirror"
			}
		} else if !os.IsNotExist(err) {
			return "unreadable_mirror"
		}
	}
	return ""
}

func groupEntry(g retentionGroup, reason string) RetentionEntry {
	entry := RetentionEntry{Group: g.key, Reason: reason, Bytes: g.bytes, MirrorBytes: g.mirrorBytes}
	for _, s := range g.sessions {
		entry.Sessions = append(entry.Sessions, s.SessionKey)
	}
	return entry
}

func archivePhysicalBytes(a retentionArchive) int64 {
	n := a.Bytes
	if info, err := os.Lstat(a.Path + ".metadata.json"); err == nil && info.Mode().IsRegular() {
		n += info.Size()
	}
	return n
}
func retiredBytesTotal(sessions []retentionSession, retired map[string]int64) int64 {
	var n int64
	for _, s := range sessions {
		n += retired[s.SessionKey]
	}
	return n
}

func buildRetentionPlan(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) (RetentionPlan, error) {
	plan := RetentionPlan{Actions: []RetentionEntry{}, Protected: []RetentionEntry{}}
	sessions, err := loadRetentionSessions(ctx, db)
	if err != nil {
		return plan, err
	}
	refs := map[string]bool{}
	for _, s := range sessions {
		for _, a := range s.archives {
			refs[a.Path] = true
			refs[a.Path+".metadata.json"] = true
		}
	}
	pending, err := pendingDeletions(ctx, db)
	if err != nil {
		return plan, err
	}
	for _, d := range pending {
		if d.Root == "archive" {
			refs[filepath.Join(cfg.Paths.Archive, d.Path)] = true
			refs[filepath.Join(cfg.Paths.Archive, d.Quarantine)] = true
		}
	}
	err = filepath.WalkDir(cfg.Paths.Archive, func(name string, entry fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && name == cfg.Paths.Archive {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		plan.ArchiveBytes += info.Size()
		if !refs[name] && cfg.Retention.OrphanGrace.Duration > 0 && recognizedOrphan(name) {
			hash, size, err := physicalFile(ctx, name)
			if err != nil {
				return err
			}
			d, err := newDeletion(cfg, "archive", name, hash, "orphan", size, now.Add(cfg.Retention.OrphanGrace.Duration))
			if err != nil {
				return err
			}
			plan.orphan = append(plan.orphan, d)
			plan.Protected = append(plan.Protected, RetentionEntry{Group: name, Reason: "orphan_grace", Bytes: size})
		} else if !refs[name] {
			plan.Protected = append(plan.Protected, RetentionEntry{Group: name, Reason: "unmanaged_archive_file", Bytes: info.Size()})
		}
		return nil
	})
	if err != nil {
		return plan, err
	}
	plan.AvailableBytes, err = diskspace.Available(cfg.Paths.Archive)
	if err != nil {
		return plan, err
	}
	plan.MirrorAvailableBytes, err = diskspace.Available(cfg.Paths.Mirror)
	if err != nil {
		return plan, err
	}
	plan.SharedFilesystem, err = diskspace.SameFilesystem(cfg.Paths.Archive, cfg.Paths.Mirror)
	if err != nil {
		return plan, err
	}
	for _, d := range pending {
		if d.Group != "" {
			plan.Protected = append(plan.Protected, RetentionEntry{Group: d.Group, Reason: "reservation_recovery_required", MirrorBytes: d.Size})
			continue
		}
		due, err := validClock(d.NotBefore)
		if err != nil {
			return plan, err
		}
		if d.Root == "archive" && !now.Before(due) {
			bytes := pendingPhysicalBytes(ctx, cfg, d)
			plan.PlannedBytes += bytes
			plan.Actions = append(plan.Actions, RetentionEntry{Group: d.ID, Reason: "pending_" + d.Reason, Bytes: bytes})
		} else if d.Root == "archive" {
			plan.Protected = append(plan.Protected, RetentionEntry{Group: d.ID, Reason: "pending_grace", Bytes: pendingPhysicalBytes(ctx, cfg, d)})
		} else if d.Root == "mirror" && !now.Before(due) {
			plan.PlannedMirrorBytes += pendingPhysicalBytes(ctx, cfg, d)
		}
	}
	retiredBytes := map[string]int64{}
	// Legacy versioned rows are maintained even when no new archive was written.
	for _, s := range sessions {
		if len(s.archives) < 2 || s.Status != constants.ArchiveStatusArchived {
			continue
		}
		if verifyCurrentArchive(ctx, s.compactSession) != nil {
			continue
		}
		for i, a := range s.archives {
			if a.Path == s.LatestArchivePath {
				continue
			}
			keepCount := cfg.Archive.VersionRetention > 0 && i < cfg.Archive.VersionRetention
			created, err := validClock(a.Created)
			if err != nil && cfg.Archive.Policy != "replace" {
				continue
			}
			keepAge := cfg.Archive.VersionRetentionDays > 0 && now.Sub(created) < time.Duration(cfg.Archive.VersionRetentionDays)*24*time.Hour
			if cfg.Archive.Policy == "replace" || ((cfg.Archive.VersionRetention > 0 || cfg.Archive.VersionRetentionDays > 0) && !keepCount && !keepAge) {
				a.Backing = proofFor(s)
				plan.retire = append(plan.retire, a)
				plan.Actions = append(plan.Actions, RetentionEntry{Group: a.Key, Reason: "version_retention", Bytes: archivePhysicalBytes(a)})
				plan.PlannedBytes += archivePhysicalBytes(a)
				retiredBytes[s.SessionKey] += archivePhysicalBytes(a)
			}
		}
	}
	groups := groupSessions(sessions)
	reserved, err := reservedMemberPaths(ctx, db)
	if err != nil {
		return plan, err
	}
	for i := range groups {
		for _, s := range groups[i].sessions {
			if reserved[s.MirrorPath] {
				groups[i].protected = "reservation_recovery_required"
			}
		}
	}
	retainedSources := map[string]int{}
	for _, s := range sessions {
		if s.Role != artifact.SessionNameIndex {
			retainedSources[sessionSource(s)]++
		}
	}
	selected := map[string]bool{}
	drainedSources := map[string]bool{}
	sourcePending := map[string]bool{}
	sourceChecked := map[string]bool{}
	for i := range groups {
		g := &groups[i]
		for _, s := range g.sessions {
			if info, err := os.Lstat(s.MirrorPath); err == nil && info.Mode().IsRegular() {
				g.mirrorBytes += info.Size()
			}
			for _, a := range s.archives {
				g.bytes += a.Bytes
				if info, err := os.Lstat(a.Path + ".metadata.json"); err == nil && info.Mode().IsRegular() {
					g.bytes += info.Size()
				}
			}
		}
		g.bytes -= retiredBytesTotal(g.sessions, retiredBytes)
		if cfg.Retention.ArchiveMaxAge.Duration == 0 && cfg.Retention.ArchiveMaxBytes.Bytes == 0 && cfg.Retention.MinFreeBytes.Bytes == 0 {
			continue
		}
		g.protected = groupProtection(cfg, g, now)
		if g.protected == "source_global_index" && len(g.sessions) == 1 && retainedSources[sessionSource(g.sessions[0])] == 0 {
			g.protected = ""
			g.protected = groupProtection(cfg, g, now)
		}
		if g.protected != "" {
			plan.Protected = append(plan.Protected, groupEntry(*g, g.protected))
			continue
		}
		ttl := cfg.Retention.ArchiveMaxAge.Duration > 0 && now.Sub(g.anchor) >= cfg.Retention.ArchiveMaxAge.Duration
		pressure := groupRelievesPressure(cfg, plan, *g)
		if !ttl && !pressure && retentionPressure(cfg, plan) {
			plan.Protected = append(plan.Protected, groupEntry(*g, "no_relief_on_constrained_filesystem"))
			continue
		}
		if ttl || pressure {
			source := sessionSource(g.sessions[0])
			if !sourceChecked[source] {
				pending, err := sourceHasUnindexedArtifacts(ctx, db, cfg, g.sessions[0])
				if err != nil {
					return plan, err
				}
				sourcePending[source] = pending
				sourceChecked[source] = true
			}
			if sourcePending[source] {
				plan.Protected = append(plan.Protected, groupEntry(*g, "source_unindexed_input"))
				continue
			}
			if reason := eligibleGroup(ctx, cfg, g, now); reason != "" {
				plan.Protected = append(plan.Protected, groupEntry(*g, reason))
				continue
			}
			reason := "archive_bytes"
			if ttl {
				reason = "archive_max_age"
			} else if cfg.Retention.ArchiveMaxBytes.Bytes == 0 || plan.ArchiveBytes-plan.PlannedBytes <= cfg.Retention.ArchiveMaxBytes.Bytes {
				reason = "min_free_bytes"
			}
			plan.Actions = append(plan.Actions, groupEntry(*g, reason))
			plan.expire = append(plan.expire, g.sessions)
			plan.PlannedBytes += g.bytes
			plan.PlannedMirrorBytes += g.mirrorBytes
			for _, s := range g.sessions {
				selected[s.SessionKey] = true
				retainedSources[sessionSource(s)]--
				drainedSources[sessionSource(s)] = true
			}
		}
	}
	// A source-global index expires with the last selected source group, never as
	// one duplicate per session. Its own content clock does not renew transcripts.
	for _, s := range sessions {
		if s.Role != artifact.SessionNameIndex || selected[s.SessionKey] || retainedSources[sessionSource(s)] > 0 {
			continue
		}
		if !drainedSources[sessionSource(s)] {
			continue
		}
		if eligibleIndex(ctx, cfg, s, now) != "" {
			continue
		}
		g := retentionGroup{key: s.SessionKey, sessions: []retentionSession{s}}
		if info, err := os.Lstat(s.MirrorPath); err == nil && info.Mode().IsRegular() {
			g.mirrorBytes = info.Size()
		}
		for _, a := range s.archives {
			g.bytes += a.Bytes
			if info, err := os.Lstat(a.Path + ".metadata.json"); err == nil {
				g.bytes += info.Size()
			}
		}
		if drainedSources[sessionSource(s)] {
			plan.expire = append(plan.expire, g.sessions)
			plan.Actions = append(plan.Actions, groupEntry(g, "source_drained"))
			plan.PlannedBytes += g.bytes
			plan.PlannedMirrorBytes += g.mirrorBytes
			selected[s.SessionKey] = true
		}
	}
	plan.PressureUnsatisfied = retentionPressure(cfg, plan)
	// A global index selected with its source is no longer protected in this plan.
	filtered := plan.Protected[:0]
	for _, entry := range plan.Protected {
		if entry.Reason == "source_global_index" && selected[entry.Group] {
			continue
		}
		filtered = append(filtered, entry)
	}
	plan.Protected = filtered
	return plan, nil
}

func retentionPressure(cfg config.HubAppConfig, plan RetentionPlan) bool {
	if cfg.Retention.ArchiveMaxBytes.Bytes > 0 && plan.ArchiveBytes-plan.PlannedBytes > cfg.Retention.ArchiveMaxBytes.Bytes {
		return true
	}
	floor := cfg.Retention.MinFreeBytes.Bytes
	if floor == 0 {
		return false
	}
	if plan.SharedFilesystem {
		return plan.AvailableBytes+plan.PlannedBytes+plan.PlannedMirrorBytes < floor
	}
	return plan.AvailableBytes+plan.PlannedBytes < floor || plan.MirrorAvailableBytes+plan.PlannedMirrorBytes < floor
}

func groupRelievesPressure(cfg config.HubAppConfig, plan RetentionPlan, g retentionGroup) bool {
	if cfg.Retention.ArchiveMaxBytes.Bytes > 0 && plan.ArchiveBytes-plan.PlannedBytes > cfg.Retention.ArchiveMaxBytes.Bytes && g.bytes > 0 {
		return true
	}
	floor := cfg.Retention.MinFreeBytes.Bytes
	if floor == 0 {
		return false
	}
	if plan.SharedFilesystem {
		return plan.AvailableBytes+plan.PlannedBytes+plan.PlannedMirrorBytes < floor && g.bytes+g.mirrorBytes > 0
	}
	return plan.AvailableBytes+plan.PlannedBytes < floor && g.bytes > 0 || plan.MirrorAvailableBytes+plan.PlannedMirrorBytes < floor && g.mirrorBytes > 0
}

func pendingPhysicalBytes(ctx context.Context, cfg config.HubAppConfig, d deletion) int64 {
	root := cfg.Paths.Archive
	if d.Root == "mirror" {
		root = cfg.Paths.Mirror
	}
	for _, rel := range []string{d.Quarantine, d.Path} {
		hash, size, err := physicalFile(ctx, filepath.Join(root, rel))
		if err == nil && hash == d.Hash && size == d.Size {
			return size
		}
	}
	return 0
}

func eligibleIndex(ctx context.Context, cfg config.HubAppConfig, s retentionSession, now time.Time) string {
	s.Role = artifact.Transcript
	g := retentionGroup{sessions: []retentionSession{s}, anchor: s.anchor}
	return eligibleGroup(ctx, cfg, &g, now)
}

var archiveNameRE = regexp.MustCompile(`\.[a-f0-9]{16}\.[a-f0-9]{16}(\.[a-f0-9]{16})?(\.v[0-9]+)?\.(jsonl|json|bin)\.zst(\.metadata\.json)?$`)

func recognizedOrphan(name string) bool {
	base := filepath.Base(name)
	return archiveNameRE.MatchString(base) || strings.HasPrefix(base, ".asc-archive-") || strings.HasPrefix(base, ".asc-metadata-")
}

func applyRetentionPlan(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, plan RetentionPlan, now time.Time) error {
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		return err
	}
	for _, group := range plan.expire {
		sharedIndex := false
		for _, s := range group {
			if s.Role == artifact.SessionNameIndex {
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE sender=? AND user=? AND agent=? AND artifact_role<>?`, s.Sender, s.User, s.Agent, artifact.SessionNameIndex).Scan(&count); err != nil {
					return err
				}
				if count > 0 {
					sharedIndex = true
				}
			}
		}
		if sharedIndex {
			continue
		}
		id, ready, err := reserveExpiryGroup(ctx, db, cfg, group, now)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		if err := commitExpiryGroup(ctx, db, cfg, id, group, now); err != nil {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range plan.retire {
		if err := queueRetiredArchive(ctx, tx, cfg, a, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_archives WHERE session_key=? AND version=? AND path=?`, a.Key, a.Version, a.Path); err != nil {
			return err
		}
	}
	for _, d := range plan.orphan {
		if err := queueDeletion(ctx, tx, d, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func commitExpiryGroup(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, id string, group []retentionSession, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range group {
		key := s.ArtifactID
		if key == "" {
			key = s.SessionKey
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO expiry_tombstones(artifact_key,raw_sha256,expired_at,reason) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, key, s.LatestRawSHA256, now.UTC().Format(time.RFC3339Nano), "retention"); err != nil {
			return err
		}
		for _, a := range s.archives {
			if err := queueFile(ctx, tx, cfg, "archive", a.Path, a.Hash, "expired", now, now); err != nil {
				return err
			}
			if err := queueSidecar(ctx, tx, cfg, a.Path, "expired", now); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE session_key=? AND latest_raw_sha256=?`, s.SessionKey, s.LatestRawSHA256)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("group catalog generation changed")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pending_deletions SET group_id='' WHERE group_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_expiry_groups WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
