CREATE TABLE sessions (
  session_key TEXT PRIMARY KEY,
  agent TEXT NOT NULL CHECK (agent IN ('claude', 'codex')),
  sender TEXT NOT NULL,
  user TEXT NOT NULL,
  env TEXT NOT NULL DEFAULT '',
  host_name TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL DEFAULT '',
  mirror_path TEXT NOT NULL,
  latest_size_bytes INTEGER NOT NULL CHECK (latest_size_bytes >= 0),
  latest_mtime_unix_ns INTEGER NOT NULL,
  latest_raw_sha256 TEXT NOT NULL DEFAULT '',
  archive_status TEXT NOT NULL CHECK (archive_status IN ('hot', 'archived')),
  latest_archive_path TEXT NOT NULL DEFAULT '',
  latest_archive_sha256 TEXT NOT NULL DEFAULT '',
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  last_index_event_at TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX sessions_agent_sender_user_session_idx
  ON sessions(agent, sender, user, session_id);
CREATE INDEX sessions_archive_status_idx ON sessions(archive_status);
CREATE INDEX sessions_last_seen_at_idx ON sessions(last_seen_at);

CREATE TABLE session_archives (
  session_key TEXT NOT NULL REFERENCES sessions(session_key) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version >= 0),
  path TEXT NOT NULL,
  raw_size_bytes INTEGER NOT NULL CHECK (raw_size_bytes >= 0),
  raw_mtime_unix_ns INTEGER NOT NULL,
  raw_sha256 TEXT NOT NULL,
  archive_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL,
  superseded_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (session_key, version)
);

CREATE INDEX session_archives_created_at_idx ON session_archives(created_at);
