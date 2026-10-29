package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/config"
)

func proofFor(s retentionSession) *archiveProof {
	return &archiveProof{Path: s.LatestArchivePath, Hash: s.LatestArchiveSHA256, RawHash: s.LatestRawSHA256, RawSize: logicalSize(s.compactSession), ArtifactID: s.ArtifactID}
}

// Backfill only a verified current generation. Historical version labels cannot
// be reconstructed honestly from today's catalog and are never fabricated.
func repairCurrentArchiveMetadata(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) error {
	sessions, err := loadRetentionSessions(ctx, db)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.LatestArchivePath == "" {
			continue
		}
		if _, err := os.Lstat(s.LatestArchivePath + ".metadata.json"); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := verifyCurrentArchive(ctx, s.compactSession); err != nil {
			continue
		} // Unverified groups remain protected.
		if err := writeArchiveMetadata(ctx, db, s.SessionKey, s.LatestArchivePath, now, "legacy_backfill_current_metadata", cfg.Retention.MinFreeBytes.Bytes); err != nil {
			return err
		}
		if err := syncArchiveSidecar(s.LatestArchivePath); err != nil {
			return err
		}
	}
	return nil
}

func verifyArchiveProof(ctx context.Context, p *archiveProof, physical string) error {
	if p == nil || p.Path == "" {
		return fmt.Errorf("missing backing archive proof")
	}
	hash, _, err := physicalFile(ctx, p.Path)
	if err != nil {
		return err
	}
	if hash != p.Hash {
		return fmt.Errorf("backing archive checksum mismatch")
	}
	if err := verifyZstd(ctx, p.Path, p.RawHash, p.RawSize); err != nil {
		return err
	}
	var snapshot struct {
		RawSHA      string `json:"raw_sha256"`
		PhysicalSHA string `json:"physical_sha256"`
		ArtifactID  string `json:"artifact_id"`
	}
	err = artifact.Read(ctx, p.Path+".metadata.json", artifact.Plain, func(r io.Reader) error { return json.NewDecoder(io.LimitReader(r, 128*1024)).Decode(&snapshot) })
	if err != nil {
		return err
	}
	if snapshot.RawSHA != p.RawHash || (p.ArtifactID != "" && snapshot.ArtifactID != p.ArtifactID) || (physical != "" && snapshot.PhysicalSHA != physical) {
		return fmt.Errorf("backing metadata snapshot mismatch")
	}
	return nil
}

func validateDeletionBacking(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, d deletion) error {
	switch d.Reason {
	case "archived_plain", "replaced", "superseded":
	default:
		return nil
	}
	rootName := cfg.Paths.Archive
	if d.Root == "mirror" {
		rootName = cfg.Paths.Mirror
	}
	matched := false
	for _, relative := range []string{d.Quarantine, d.Path} {
		hash, size, err := physicalFile(ctx, filepath.Join(rootName, relative))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && hash == d.Hash && size == d.Size {
			matched = true
		}
	}
	if !matched {
		return nil
	} // No selected generation remains to unlink.
	proof := d.Backing
	if proof == nil && d.Reason == "archived_plain" {
		// Unknown/legacy intents cannot bypass archive verification on recovery.
		sessions, err := loadRetentionSessions(ctx, db)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.MirrorPath == filepath.Join(cfg.Paths.Mirror, d.Path) && physicalHash(s.compactSession) == d.Hash {
				proof = proofFor(s)
				break
			}
		}
	}
	physical := ""
	if d.Reason == "archived_plain" {
		physical = d.Hash
	}
	return verifyArchiveProof(ctx, proof, physical)
}

func queueBackedFile(ctx context.Context, writer sqlWriter, cfg config.HubAppConfig, kind, name, hash, reason string, backing *archiveProof, now time.Time) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("nonregular backed deletion")
	}
	d, err := newDeletion(cfg, kind, name, hash, reason, info.Size(), now)
	if err != nil {
		return err
	}
	d.Backing = backing
	return queueDeletion(ctx, writer, d, now)
}

func queueRetiredArchive(ctx context.Context, writer sqlWriter, cfg config.HubAppConfig, a retentionArchive, now time.Time) error {
	if err := queueBackedFile(ctx, writer, cfg, "archive", a.Path, a.Hash, "superseded", a.Backing, now); err != nil {
		return err
	}
	name := a.Path + ".metadata.json"
	hash, _, err := physicalFile(ctx, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return queueBackedFile(ctx, writer, cfg, "archive", name, hash, "superseded", a.Backing, now)
}
