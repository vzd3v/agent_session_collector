package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/config"
)

func ensureQuarantine(rootName string) error {
	if err := os.MkdirAll(filepath.Join(rootName, ".asc-trash"), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(rootName, ".asc-trash"))
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("unsafe hub quarantine")
	}
	return syncStorageParents(rootName, filepath.Join(rootName, ".asc-trash"))
}

// An uncommitted group reservation is always rolled back after interruption.
// Committed groups have no row here: their ordinary deletion intents finish.
func recoverExpiryGroups(ctx context.Context, db *sql.DB, cfg config.HubAppConfig) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM pending_expiry_groups ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := restoreExpiryGroup(ctx, db, cfg, id); err != nil {
			return err
		}
	}
	return nil
}

func reservedMemberPaths(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT member_paths_json FROM pending_expiry_groups`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		var paths []string
		if err := json.Unmarshal([]byte(text), &paths); err != nil {
			return nil, err
		}
		for _, p := range paths {
			out[p] = true
		}
	}
	return out, rows.Err()
}

func restoreExpiryGroup(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, id string) error {
	pending, err := pendingDeletions(ctx, db)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(cfg.Paths.Mirror)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var problems []error
	for _, d := range pending {
		if d.Group == id {
			if err := restoreReservationFile(root, cfg.Paths.Mirror, d); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_deletions WHERE group_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_expiry_groups WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func restoreReservationFile(root *os.Root, rootName string, d deletion) error {
	if _, err := root.Lstat(d.Quarantine); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := root.Link(d.Quarantine, d.Path); err != nil {
		return fmt.Errorf("preserved reservation at %s: cannot overwrite a newer arrival: %w", filepath.Join(rootName, d.Quarantine), err)
	}
	if err := root.Remove(d.Quarantine); err != nil {
		return err
	}
	return errors.Join(syncDirectory(filepath.Join(rootName, filepath.Dir(d.Path))), syncDirectory(filepath.Join(rootName, ".asc-trash")))
}

func reserveExpiryGroup(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, group []retentionSession, now time.Time) (string, bool, error) {
	if len(group) == 0 {
		return "", false, nil
	}
	if reason := eligibleGroup(ctx, cfg, &retentionGroup{sessions: group}, now); reason != "" {
		return "", false, nil
	}
	if err := ensureQuarantine(cfg.Paths.Mirror); err != nil {
		return "", false, err
	}
	var paths []string
	var candidates []deletion
	for _, s := range group {
		paths = append(paths, s.MirrorPath)
		info, err := os.Lstat(s.MirrorPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		d, err := newDeletion(cfg, "mirror", s.MirrorPath, physicalHash(s.compactSession), "expired", info.Size(), now)
		if err != nil {
			return "", false, err
		}
		candidates = append(candidates, d)
	}
	body, err := json.Marshal(paths)
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(append(body, []byte(now.UTC().Format(time.RFC3339Nano))...))
	id := hex.EncodeToString(sum[:])
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_expiry_groups(id,member_paths_json,created_at) VALUES(?,?,?)`, id, string(body), now.UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	for i := range candidates {
		candidates[i].Group = id
		if err := queueDeletion(ctx, tx, candidates[i], now); err != nil {
			_ = tx.Rollback()
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	root, err := os.OpenRoot(cfg.Paths.Mirror)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = root.Close() }()
	abort := func() (string, bool, error) { err := restoreExpiryGroup(ctx, db, cfg, id); return "", false, err }
	for _, d := range candidates {
		hash, size, err := physicalRoot(root, d.Path)
		if err != nil || hash != d.Hash || size != d.Size {
			return abort()
		}
		if err := root.Rename(d.Path, d.Quarantine); err != nil {
			return abort()
		}
		if err := syncDirectory(filepath.Join(cfg.Paths.Mirror, filepath.Dir(d.Path))); err != nil {
			return "", false, err
		}
		if err := syncDirectory(filepath.Join(cfg.Paths.Mirror, ".asc-trash")); err != nil {
			return "", false, err
		}
		hash, size, err = physicalRoot(root, d.Quarantine)
		if err != nil || hash != d.Hash || size != d.Size {
			return abort()
		}
	}
	// The reservation epoch selects the old generations. These sequential
	// checks conservatively abort on observed conflicts; they are not a global
	// receiver lock or an atomic namespace snapshot. Later publications survive.
	for _, s := range group {
		if err := verifyCurrentArchive(ctx, s.compactSession); err != nil {
			return abort()
		}
	}
	changed, err := sourceHasUnindexedArtifacts(ctx, db, cfg, group[0])
	if err != nil {
		return "", false, err
	}
	if changed {
		return abort()
	}
	for _, name := range paths {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return abort()
		}
	}
	return id, true, nil
}

// A previously unindexed child cannot be assigned safely from the old catalog.
// Protect that source until index has incorporated all received artifact paths.
func sourceHasUnindexedArtifacts(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, s retentionSession) (bool, error) {
	if s.RelativePath == "" {
		return false, nil
	}
	sourceRoot := s.MirrorPath
	for range strings.Split(filepath.FromSlash(s.RelativePath), string(filepath.Separator)) {
		sourceRoot = filepath.Dir(sourceRoot)
	}
	if _, err := relativeUnder(cfg.Paths.Mirror, filepath.Join(sourceRoot, ".entry")); err != nil {
		return true, err
	}
	rows, err := db.QueryContext(ctx, `SELECT mirror_path FROM sessions WHERE sender=? AND user=? AND agent=?`, s.Sender, s.User, s.Agent)
	if err != nil {
		return true, err
	}
	known := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return true, err
		}
		known[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return true, err
	}
	if err := rows.Close(); err != nil {
		return true, err
	}
	changed := false
	err = filepath.WalkDir(sourceRoot, func(name string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, name)
		if err != nil {
			return err
		}
		if artifact.Role(s.Agent, filepath.ToSlash(rel)) != "" && !known[name] {
			changed = true
		}
		return nil
	})
	return changed, err
}
