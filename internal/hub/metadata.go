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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vzd3v/agent_session_collector/internal/artifact"
	"github.com/vzd3v/agent_session_collector/internal/checksum"
	"github.com/vzd3v/agent_session_collector/internal/config"
	"github.com/vzd3v/agent_session_collector/internal/diskspace"
	"github.com/vzd3v/agent_session_collector/internal/nativeid"
	sourceinfo "github.com/vzd3v/agent_session_collector/internal/source"
)

func indexSourceDescriptors(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, now time.Time) (map[string]sourceinfo.Descriptor, map[string]bool, []error) {
	sources := make(map[string]sourceinfo.Descriptor)
	invalid := make(map[string]bool)
	var problems []error
	// Absence of a new descriptor is not a request to erase previously accepted labels.
	rows, err := db.QueryContext(ctx, `SELECT sender,user,agent,host_name,env,data_class,metadata_json FROM sources`)
	if err != nil {
		return sources, invalid, []error{err}
	}
	for rows.Next() {
		var d sourceinfo.Descriptor
		var labels string
		d.Version = sourceinfo.Version
		d.Provenance = "last_accepted_descriptor"
		if err := rows.Scan(&d.Sender, &d.User, &d.Agent, &d.HostName, &d.Env, &d.DataClass, &labels); err != nil {
			_ = rows.Close()
			return sources, invalid, []error{err}
		}
		if err := json.Unmarshal([]byte(labels), &d.Metadata); err != nil {
			_ = rows.Close()
			return sources, invalid, []error{err}
		}
		id := sourceinfo.ID(d.Sender, d.User, d.Agent)
		sources[id] = d
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return sources, invalid, []error{err}
	}
	for _, sender := range cfg.Receive.Sender {
		root := filepath.Join(cfg.Paths.Mirror, sender.Mirror)
		descriptorRoot := filepath.Join(root, ".asc", "sources")
		err := filepath.WalkDir(descriptorRoot, func(name string, entry fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) && name == descriptorRoot {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(descriptorRoot, name)
			if err != nil {
				return err
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) != 2 || !strings.HasSuffix(parts[1], ".json") {
				problems = append(problems, fmt.Errorf("invalid source descriptor path %s", name))
				return nil
			}
			user, agent := parts[0], strings.TrimSuffix(parts[1], ".json")
			id := sourceinfo.ID(sender.SenderID, user, agent)
			var descriptor sourceinfo.Descriptor
			err = artifact.Read(ctx, name, artifact.Plain, func(r io.Reader) error {
				var decodeErr error
				descriptor, decodeErr = sourceinfo.Decode(r)
				return decodeErr
			})
			if err == nil && (descriptor.Sender != sender.SenderID || descriptor.User != user || descriptor.Agent != agent) {
				err = fmt.Errorf("descriptor identity does not match authenticated sender/path")
			}
			if err == nil {
				err = validateSourcePolicy(cfg, descriptor)
			}
			if err != nil {
				invalid[id] = true
				problems = append(problems, fmt.Errorf("source descriptor %s: %w", name, err))
				return nil
			}
			body, err := descriptor.Encode()
			if err != nil {
				return err
			}
			if descriptor.Metadata == nil {
				descriptor.Metadata = map[string]string{}
			}
			metadata, err := json.Marshal(descriptor.Metadata)
			if err != nil {
				return err
			}
			digest := sourceinfo.Digest(body)
			_, err = db.ExecContext(ctx, `INSERT INTO sources(source_id,sender,user,agent,host_name,env,data_class,metadata_json,descriptor_sha256,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET host_name=excluded.host_name,env=excluded.env,data_class=excluded.data_class,metadata_json=excluded.metadata_json,descriptor_sha256=excluded.descriptor_sha256,updated_at=excluded.updated_at WHERE sources.descriptor_sha256<>excluded.descriptor_sha256`, id, descriptor.Sender, descriptor.User, descriptor.Agent, descriptor.HostName, descriptor.Env, descriptor.DataClass, string(metadata), digest, now.Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			// Source metadata has its own clock. In particular, this must not refresh content age.
			_, err = db.ExecContext(ctx, `UPDATE sessions SET source_id=?,host_name=?,env=?,data_class=?,metadata_json=?,metadata_sha256=? WHERE sender=? AND user=? AND agent=?`, id, descriptor.HostName, descriptor.Env, descriptor.DataClass, string(metadata), digest, descriptor.Sender, descriptor.User, descriptor.Agent)
			if err != nil {
				return err
			}
			descriptor.Provenance = "present_descriptor"
			delete(invalid, id)
			sources[id] = descriptor
			return nil
		})
		if err != nil {
			problems = append(problems, fmt.Errorf("index source descriptors for %s: %w", sender.SenderID, err))
		}
	}
	for id, d := range sources {
		if !invalid[id] {
			if err := validateSourcePolicy(cfg, d); err != nil {
				invalid[id] = true
				problems = append(problems, fmt.Errorf("last accepted source %s: %w", id, err))
			}
		}
	}
	return sources, invalid, problems
}

func validateSourcePolicy(cfg config.HubAppConfig, d sourceinfo.Descriptor) error {
	class, ok := cfg.DataClasses[d.DataClass]
	allowed := false
	for _, hub := range class.RawHubAllowed {
		if hub == cfg.HubID {
			allowed = true
		}
	}
	if !ok || class.RedactionRequired || !allowed {
		return fmt.Errorf("descriptor data class is not permitted for this raw hub")
	}
	return nil
}

type projectionCache struct {
	key, rawHash, physicalHash, evidence, lastEventMetadata, projectionHash, lastEventRaw string
	rawSize                                                                               int64
	version                                                                               int
}

var errArtifactChanged = errors.New("artifact changed during projection")

func decodedFileLimit(cfg config.HubAppConfig) int64 {
	if cfg.Index.MaxDecodedFileSize.Bytes > 0 {
		return cfg.Index.MaxDecodedFileSize.Bytes
	}
	if cfg.Receive.MaxFileSize.Bytes > 0 {
		return cfg.Receive.MaxFileSize.Bytes
	}
	return 2 << 30
}

func prepareArtifact(ctx context.Context, db *sql.DB, file *mirrorFile, limit int64) error {
	var cached projectionCache
	err := db.QueryRowContext(ctx, `SELECT session_key,latest_raw_sha256,latest_physical_sha256,latest_raw_size_bytes,identity_evidence_json,projection_version,last_index_event_metadata_sha256,projection_raw_sha256,last_index_event_raw_sha256 FROM sessions WHERE artifact_id=? OR session_key=? ORDER BY artifact_id DESC LIMIT 1`, file.ArtifactID, buildSessionKey(*file)).Scan(&cached.key, &cached.rawHash, &cached.physicalHash, &cached.rawSize, &cached.evidence, &cached.version, &cached.lastEventMetadata, &cached.projectionHash, &cached.lastEventRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load artifact projection: %w", err)
	}
	if err == nil {
		file.LegacyKey = cached.key
		file.LastEventMetadataSHA256 = cached.lastEventMetadata
		file.LastEventRawSHA256 = cached.lastEventRaw
		if cached.version == nativeid.Version && cached.physicalHash == file.PhysicalSHA256 && cached.rawHash != "" && cached.projectionHash == cached.rawHash && json.Unmarshal([]byte(cached.evidence), &file.Native) == nil {
			file.RawSHA256 = cached.rawHash
			file.RawSizeBytes = cached.rawSize
			setConversation(file)
			return nil
		}
	}
	if err := projectArtifact(ctx, file, file.MirrorPath, file.Encoding, limit); err != nil {
		return err
	}
	// Projection and physical metadata must describe the same observed generation.
	physical, err := checksum.FileSHA256(file.MirrorPath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(file.MirrorPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || physical != file.PhysicalSHA256 || info.Size() != file.SizeBytes || info.ModTime().UnixNano() != file.MtimeUnixNS {
		return fmt.Errorf("%w: %s", errArtifactChanged, file.MirrorPath)
	}
	if file.Encoding == artifact.Plain && file.RawSHA256 != physical {
		return fmt.Errorf("%w: logical read differs from physical generation: %s", errArtifactChanged, file.MirrorPath)
	}
	setConversation(file)
	return nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func projectArtifact(ctx context.Context, file *mirrorFile, name, encoding string, limit int64) error {
	hash := sha256.New()
	count := &countWriter{}
	err := artifact.Read(ctx, name, encoding, func(r io.Reader) error {
		stream := io.TeeReader(io.LimitReader(r, limit+1), io.MultiWriter(hash, count))
		file.Native = nativeid.Parse(stream, file.Agent, file.RelativePath, file.Role)
		_, err := io.Copy(io.Discard, stream)
		if count.n > limit {
			return fmt.Errorf("logical artifact exceeds decoded byte limit %d", limit)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("decode artifact %s: %w", name, err)
	}
	file.RawSHA256 = hex.EncodeToString(hash.Sum(nil))
	file.RawSizeBytes = count.n
	return nil
}

func setConversation(file *mirrorFile) {
	file.ConversationID = ""
	if file.Agent == "codex" && file.Role == artifact.Transcript {
		file.ConversationID = file.Native.SessionID
	}
}

func archivedOnlyArtifacts(ctx context.Context, db *sql.DB, cfg config.HubAppConfig, sources map[string]sourceinfo.Descriptor, invalid map[string]bool) ([]mirrorFile, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key,sender,user,agent,session_id,mirror_path,latest_size_bytes,latest_mtime_unix_ns,latest_raw_sha256,latest_physical_sha256,latest_raw_size_bytes,source_encoding,relative_path,artifact_role,latest_archive_path,latest_archive_sha256,identity_evidence_json,projection_version,last_index_event_metadata_sha256,projection_raw_sha256,last_index_event_raw_sha256 FROM sessions WHERE archive_status='archived' AND latest_archive_path<>''`)
	if err != nil {
		return nil, err
	}
	type archived struct {
		file                                 mirrorFile
		path, hash, evidence, projectionHash string
		version                              int
	}
	var records []archived
	for rows.Next() {
		var record archived
		f := &record.file
		if err := rows.Scan(&f.LegacyKey, &f.Sender, &f.User, &f.Agent, &f.SessionID, &f.MirrorPath, &f.SizeBytes, &f.MtimeUnixNS, &f.RawSHA256, &f.PhysicalSHA256, &f.RawSizeBytes, &f.Encoding, &f.RelativePath, &f.Role, &record.path, &record.hash, &record.evidence, &record.version, &f.LastEventMetadataSHA256, &record.projectionHash, &f.LastEventRawSHA256); err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	var files []mirrorFile
	var problems []error
	for _, record := range records {
		f := record.file
		if _, err := os.Lstat(f.MirrorPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			problems = append(problems, err)
			continue
		}
		f.SourceID = sourceinfo.ID(f.Sender, f.User, f.Agent)
		if invalid[f.SourceID] {
			continue
		}
		if f.RelativePath == "" {
			for _, sender := range cfg.Receive.Sender {
				if sender.SenderID == f.Sender {
					rel, err := filepath.Rel(filepath.Join(cfg.Paths.Mirror, sender.Mirror, f.User, f.Agent), f.MirrorPath)
					if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						f.RelativePath = filepath.ToSlash(rel)
					}
					break
				}
			}
		}
		if f.RelativePath == "" {
			continue
		} // No portable path evidence for an unknown historical namespace.
		f.ArtifactID = sourceinfo.ArtifactID(f.SourceID, f.RelativePath)
		f.Role = artifact.Role(f.Agent, f.RelativePath)
		f.ArchivedOnly = true
		if descriptor, ok := sources[f.SourceID]; ok {
			f.Descriptor = descriptor
			body, _ := descriptor.Encode()
			f.MetadataSHA256 = sourceinfo.Digest(body)
		}
		if record.version == nativeid.Version && record.projectionHash == f.RawSHA256 && json.Unmarshal([]byte(record.evidence), &f.Native) == nil {
			setConversation(&f)
			files = append(files, f)
			continue
		}
		sum, err := checksum.FileSHA256(record.path)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if sum != record.hash {
			problems = append(problems, fmt.Errorf("archive checksum mismatch during projection: %s", record.path))
			continue
		}
		expected := f.RawSHA256
		if err := projectArtifact(ctx, &f, record.path, artifact.Zstd, decodedFileLimit(cfg)); err != nil {
			problems = append(problems, err)
			continue
		}
		if f.RawSHA256 != expected {
			problems = append(problems, fmt.Errorf("archive logical checksum mismatch during projection: %s", record.path))
			continue
		}
		setConversation(&f)
		files = append(files, f)
	}
	return files, errors.Join(problems...)
}

func storeArtifactMetadata(ctx context.Context, db *sql.DB, file mirrorFile, key string) error {
	evidence, err := json.Marshal(file.Native)
	if err != nil {
		return err
	}
	metadata := file.Descriptor.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	labels, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	eventHash := file.LastEventMetadataSHA256
	_, err = db.ExecContext(ctx, `UPDATE sessions SET source_id=?,artifact_id=?,relative_path=?,artifact_role=?,source_encoding=?,latest_physical_sha256=?,latest_raw_size_bytes=?,host_name=?,env=?,data_class=?,metadata_json=?,metadata_sha256=?,last_index_event_metadata_sha256=?,native_session_id=?,native_agent_id=?,native_root_id=?,native_parent_id=?,native_fork_id=?,identity_evidence_json=?,projection_version=?,projection_raw_sha256=? WHERE session_key=?`, file.SourceID, file.ArtifactID, file.RelativePath, file.Role, file.Encoding, file.PhysicalSHA256, file.RawSizeBytes, file.Descriptor.HostName, file.Descriptor.Env, file.Descriptor.DataClass, string(labels), file.MetadataSHA256, eventHash, file.Native.SessionID, file.Native.AgentID, file.Native.RootID, file.Native.ParentID, file.Native.ForkID, string(evidence), nativeid.Version, file.RawSHA256, key)
	if err != nil {
		return err
	}
	provenance := file.Descriptor.Provenance
	if provenance == "" {
		provenance = "unknown"
	}
	_, err = db.ExecContext(ctx, `UPDATE sessions SET source_metadata_provenance=? WHERE session_key=?`, provenance, key)
	return err
}

var errArchiveMetadataNeedsIndex = errors.New("archive metadata needs indexed identity; run asc-hub index first")

// Archive sidecars are portable snapshots, separate from mutable current-source labels.
func writeArchiveMetadata(ctx context.Context, db *sql.DB, key, archivePath string, now time.Time, provenance string, minFree ...int64) error {
	name := archivePath + ".metadata.json"
	if info, err := os.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive metadata snapshot is not regular")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var sourceID, artifactID, relative, role, encoding, rawHash, physicalHash, nativeJSON, metadataJSON, host, env, class, sender, user, agent, sourceProvenance string
	var rawSize, physicalSize int64
	var projectionVersion int
	var projectionHash string
	err := db.QueryRowContext(ctx, `SELECT source_id,artifact_id,relative_path,artifact_role,source_encoding,latest_raw_sha256,latest_physical_sha256,latest_raw_size_bytes,latest_size_bytes,identity_evidence_json,metadata_json,host_name,env,data_class,sender,user,agent,source_metadata_provenance,projection_version,projection_raw_sha256 FROM sessions WHERE session_key=?`, key).Scan(&sourceID, &artifactID, &relative, &role, &encoding, &rawHash, &physicalHash, &rawSize, &physicalSize, &nativeJSON, &metadataJSON, &host, &env, &class, &sender, &user, &agent, &sourceProvenance, &projectionVersion, &projectionHash)
	if err != nil {
		return err
	}
	if sourceID == "" || artifactID == "" || relative == "" || projectionVersion != nativeid.Version || projectionHash != rawHash {
		return errArchiveMetadataNeedsIndex
	}
	snapshot := struct {
		Version                  int             `json:"version"`
		CapturedAt               string          `json:"captured_at"`
		Provenance               string          `json:"provenance"`
		SourceID                 string          `json:"source_id"`
		ArtifactID               string          `json:"artifact_id"`
		Sender                   string          `json:"sender"`
		User                     string          `json:"user"`
		Agent                    string          `json:"agent"`
		RelativePath             string          `json:"relative_path"`
		Role                     string          `json:"role"`
		Encoding                 string          `json:"source_encoding"`
		RawSHA                   string          `json:"raw_sha256"`
		PhysicalSHA              string          `json:"physical_sha256"`
		RawSize                  int64           `json:"raw_size_bytes"`
		PhysicalSize             int64           `json:"physical_size_bytes"`
		Native                   json.RawMessage `json:"native_identity"`
		Metadata                 json.RawMessage `json:"metadata"`
		Host                     string          `json:"host_name"`
		Env                      string          `json:"env"`
		Class                    string          `json:"data_class"`
		SourceMetadataProvenance string          `json:"source_metadata_provenance"`
	}{Version: 1, CapturedAt: now.UTC().Format(time.RFC3339Nano), Provenance: provenance, SourceID: sourceID, ArtifactID: artifactID, Sender: sender, User: user, Agent: agent, RelativePath: relative, Role: role, Encoding: encoding, RawSHA: rawHash, PhysicalSHA: physicalHash, RawSize: rawSize, PhysicalSize: physicalSize, Native: json.RawMessage(nativeJSON), Metadata: json.RawMessage(metadataJSON), Host: host, Env: env, Class: class, SourceMetadataProvenance: sourceProvenance}
	body, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	// Existing snapshots keep their original provenance, even after source labels change.
	floor := int64(0)
	if len(minFree) > 0 {
		floor = minFree[0]
	}
	if err := diskspace.Check(filepath.Dir(name), floor, int64(len(body))+1); err != nil {
		return fmt.Errorf("archive metadata admission: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".asc-metadata-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_, writeErr := tmp.Write(append(body, '\n'))
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Rename(tmpName, name)
}
