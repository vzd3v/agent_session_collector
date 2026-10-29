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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/config"
)

type deletion struct {
	ID, Root, Path, Quarantine, Hash, Reason, NotBefore string
	Size                                                int64
	Group                                               string
	Backing                                             *archiveProof
}

type archiveProof struct {
	Path, Hash, RawHash, ArtifactID string
	RawSize                         int64
}

type sqlWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func relativeUnder(root, name string) (string, error) {
	rel, err := filepath.Rel(root, name)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path is outside storage root: %s", name)
	}
	return rel, nil
}

func physicalFile(ctx context.Context, name string) (string, int64, error) {
	h := sha256.New()
	var size int64
	err := artifact.Read(ctx, name, artifact.Plain, func(r io.Reader) error { var err error; size, err = io.Copy(h, r); return err })
	return hex.EncodeToString(h.Sum(nil)), size, err
}

func physicalRoot(root *os.Root, name string) (string, int64, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("nonregular deletion candidate")
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), size, err
}

func newDeletion(cfg config.HubAppConfig, kind, name, hash, reason string, size int64, notBefore time.Time) (deletion, error) {
	root := cfg.Paths.Archive
	if kind == "mirror" {
		root = cfg.Paths.Mirror
	} else if kind != "archive" {
		return deletion{}, fmt.Errorf("unknown deletion root")
	}
	rel, err := relativeUnder(root, name)
	if err != nil {
		return deletion{}, err
	}
	if strings.Split(rel, string(filepath.Separator))[0] == ".asc-trash" {
		return deletion{}, fmt.Errorf("cannot enqueue quarantine as source")
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + rel + "\x00" + hash))
	id := hex.EncodeToString(sum[:])
	return deletion{ID: id, Root: kind, Path: rel, Quarantine: filepath.Join(".asc-trash", id), Hash: hash, Size: size, Reason: reason, NotBefore: notBefore.UTC().Format(time.RFC3339Nano)}, nil
}

func queueDeletion(ctx context.Context, writer sqlWriter, d deletion, now time.Time) error {
	if len(d.Hash) != 64 {
		return fmt.Errorf("refuse deletion without SHA256")
	}
	backing := ""
	if d.Backing != nil {
		body, err := json.Marshal(d.Backing)
		if err != nil {
			return err
		}
		backing = string(body)
	}
	_, err := writer.ExecContext(ctx, `INSERT INTO pending_deletions(id,root_kind,relative_path,quarantine_path,sha256,size_bytes,reason,not_before,created_at,group_id,backing_json) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(root_kind,relative_path,sha256) DO NOTHING`, d.ID, d.Root, d.Path, d.Quarantine, d.Hash, d.Size, d.Reason, d.NotBefore, now.UTC().Format(time.RFC3339Nano), d.Group, backing)
	return err
}

func queueFile(ctx context.Context, writer sqlWriter, cfg config.HubAppConfig, kind, name, hash, reason string, now, timeDue time.Time) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse nonregular deletion: %s", name)
	}
	d, err := newDeletion(cfg, kind, name, hash, reason, info.Size(), timeDue)
	if err != nil {
		return err
	}
	return queueDeletion(ctx, writer, d, now)
}

func queueSidecar(ctx context.Context, writer sqlWriter, cfg config.HubAppConfig, name, reason string, now time.Time) error {
	name += ".metadata.json"
	hash, size, err := physicalFile(ctx, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	d, err := newDeletion(cfg, "archive", name, hash, reason, size, now)
	if err != nil {
		return err
	}
	return queueDeletion(ctx, writer, d, now)
}

func pendingDeletions(ctx context.Context, db *sql.DB) ([]deletion, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,root_kind,relative_path,quarantine_path,sha256,size_bytes,reason,not_before,group_id,backing_json FROM pending_deletions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []deletion
	for rows.Next() {
		var d deletion
		var backing string
		if err := rows.Scan(&d.ID, &d.Root, &d.Path, &d.Quarantine, &d.Hash, &d.Size, &d.Reason, &d.NotBefore, &d.Group, &backing); err != nil {
			return nil, err
		}
		if backing != "" {
			d.Backing = &archiveProof{}
			if err := json.Unmarshal([]byte(backing), d.Backing); err != nil {
				return nil, err
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func syncDirectory(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func syncStorageParents(root, name string) error {
	if _, err := relativeUnder(root, filepath.Join(name, ".entry")); err != nil {
		return err
	}
	for {
		if err := syncDirectory(name); err != nil {
			return err
		}
		if name == filepath.Clean(root) {
			return syncDirectory(filepath.Dir(name))
		}
		name = filepath.Dir(name)
	}
}

// Quarantine is owned by the hub, outside every sender-writable subtree. The
// intent survives catalog retirement and each stage can be replayed after a crash.
func drainDeletions(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) (int, error) {
	pending, err := pendingDeletions(ctx, db)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, d := range pending {
		if d.Group != "" {
			continue
		}
		due, err := time.Parse(time.RFC3339Nano, d.NotBefore)
		if err != nil {
			return removed, err
		}
		if now.Before(due) {
			continue
		}
		rootName := cfg.Paths.Archive
		if d.Root == "mirror" {
			rootName = cfg.Paths.Mirror
		}
		name := filepath.Join(rootName, d.Path)
		// Orphans may have become referenced after a failed/partial archive publish.
		if d.Root == "archive" {
			var refs int
			archiveName := strings.TrimSuffix(name, ".metadata.json")
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_archives WHERE path=?`, archiveName).Scan(&refs); err != nil {
				return removed, err
			}
			if refs > 0 {
				if _, err := db.ExecContext(ctx, `DELETE FROM pending_deletions WHERE id=?`, d.ID); err != nil {
					return removed, err
				}
				continue
			}
		}
		if err := ensureQuarantine(rootName); err != nil {
			return removed, err
		}
		root, err := os.OpenRoot(rootName)
		if err != nil {
			return removed, err
		}
		if err := validateDeletionBacking(ctx, db, cfg, d); err != nil {
			restoreErr := restoreReservationFile(root, rootName, d)
			_ = root.Close()
			return removed, errors.Join(fmt.Errorf("preserve backed deletion: %w", err), restoreErr)
		}
		done, err := applyDeletion(ctx, root, rootName, d)
		closeErr := root.Close()
		if err != nil || closeErr != nil {
			return removed, errors.Join(err, closeErr)
		}
		if done {
			if _, err := db.ExecContext(ctx, `DELETE FROM pending_deletions WHERE id=?`, d.ID); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func applyDeletion(ctx context.Context, root *os.Root, rootName string, d deletion) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := relativeUnder(rootName, filepath.Join(rootName, d.Path)); err != nil {
		return false, err
	}
	if len(d.ID) != 64 {
		return false, fmt.Errorf("invalid deletion identity")
	}
	if _, err := hex.DecodeString(d.ID); err != nil {
		return false, fmt.Errorf("invalid deletion identity: %w", err)
	}
	if strings.Split(d.Path, string(filepath.Separator))[0] == ".asc-trash" {
		return false, fmt.Errorf("invalid deletion source namespace")
	}
	if d.Quarantine != filepath.Join(".asc-trash", d.ID) {
		return false, fmt.Errorf("invalid quarantine path")
	}
	quarantine := filepath.Join(rootName, d.Quarantine)
	if _, err := root.Lstat(d.Quarantine); errors.Is(err, os.ErrNotExist) {
		hash, size, err := physicalRoot(root, d.Path)
		if errors.Is(err, os.ErrNotExist) {
			return true, syncDirectory(filepath.Join(rootName, ".asc-trash"))
		}
		if err != nil {
			return false, err
		}
		if hash != d.Hash || size != d.Size {
			return true, nil
		} // A newer receiver generation is never removed.
		if err := root.Rename(d.Path, d.Quarantine); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return true, nil
			}
			return false, err
		}
		if err := syncDirectory(filepath.Join(rootName, filepath.Dir(d.Path))); err != nil {
			return false, err
		}
		if err := syncDirectory(filepath.Join(rootName, ".asc-trash")); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	hash, size, err := physicalRoot(root, d.Quarantine)
	if err != nil {
		return false, err
	}
	if hash != d.Hash || size != d.Size {
		// An rsync rename raced our first hash. Restore with no overwrite. If a new
		// file already arrived, preserve both generations and report the quarantine.
		if err := root.Link(d.Quarantine, d.Path); err != nil {
			return false, fmt.Errorf("generation changed; preserved quarantine %s: %w", quarantine, err)
		}
		if err := root.Remove(d.Quarantine); err != nil {
			return false, err
		}
		return true, errors.Join(syncDirectory(filepath.Join(rootName, filepath.Dir(d.Path))), syncDirectory(filepath.Join(rootName, ".asc-trash")))
	}
	if err := root.Remove(d.Quarantine); err != nil {
		return false, err
	}
	return true, syncDirectory(filepath.Join(rootName, ".asc-trash"))
}

func tombstoneKey(file mirrorFile) string {
	if file.ArtifactID != "" {
		return file.ArtifactID
	}
	return buildSessionKey(file)
}

func rejectExpiredReplay(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, file mirrorFile, now time.Time) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM expiry_tombstones WHERE artifact_key IN (?,?) AND raw_sha256=?`, tombstoneKey(file), buildSessionKey(file), file.RawSHA256).Scan(&count); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if !file.ArchivedOnly {
		if err := queueFile(ctx, db, cfg, "mirror", file.MirrorPath, file.PhysicalSHA256, "expired_replay", now, now); err != nil {
			return false, err
		}
	}
	return true, nil
}
