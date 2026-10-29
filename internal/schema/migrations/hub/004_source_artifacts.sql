CREATE TABLE sources (
  source_id TEXT PRIMARY KEY,
  sender TEXT NOT NULL,
  user TEXT NOT NULL,
  agent TEXT NOT NULL,
  host_name TEXT NOT NULL,
  env TEXT NOT NULL,
  data_class TEXT NOT NULL,
  metadata_json TEXT NOT NULL,
  descriptor_sha256 TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(sender,user,agent)
);
ALTER TABLE sessions ADD COLUMN source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN artifact_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN relative_path TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN artifact_role TEXT NOT NULL DEFAULT 'transcript';
ALTER TABLE sessions ADD COLUMN source_encoding TEXT NOT NULL DEFAULT 'plain';
ALTER TABLE sessions ADD COLUMN latest_physical_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN latest_raw_size_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN data_class TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE sessions ADD COLUMN metadata_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN source_metadata_provenance TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE sessions ADD COLUMN last_index_event_metadata_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_index_event_raw_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN native_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN native_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN native_root_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN native_parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN native_fork_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN identity_evidence_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE sessions ADD COLUMN projection_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN projection_raw_sha256 TEXT NOT NULL DEFAULT '';
UPDATE sessions SET latest_physical_sha256=latest_raw_sha256, latest_raw_size_bytes=latest_size_bytes;
CREATE UNIQUE INDEX sessions_artifact_id_idx ON sessions(artifact_id) WHERE artifact_id <> '';
CREATE INDEX sessions_source_idx ON sessions(source_id);
