-- Intent is committed before a filesystem rename/unlink. No foreign key: intents
-- must survive removal of the expired catalog group.
CREATE TABLE pending_deletions (
  id TEXT PRIMARY KEY,
  root_kind TEXT NOT NULL CHECK(root_kind IN ('mirror','archive')),
  relative_path TEXT NOT NULL,
  quarantine_path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  reason TEXT NOT NULL,
  not_before TEXT NOT NULL,
  created_at TEXT NOT NULL,
  group_id TEXT NOT NULL DEFAULT '',
  backing_json TEXT NOT NULL DEFAULT '',
  UNIQUE(root_kind, relative_path, sha256)
);
CREATE TABLE pending_expiry_groups (
  id TEXT PRIMARY KEY,
  member_paths_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE expiry_tombstones (
  artifact_key TEXT NOT NULL,
  raw_sha256 TEXT NOT NULL,
  expired_at TEXT NOT NULL,
  reason TEXT NOT NULL,
  PRIMARY KEY(artifact_key, raw_sha256)
);
