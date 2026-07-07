CREATE TABLE synced_files (
  source_key TEXT PRIMARY KEY,
  agent TEXT NOT NULL CHECK (agent IN ('claude', 'codex')),
  user TEXT NOT NULL,
  source_path TEXT NOT NULL,
  session_id TEXT NOT NULL,
  size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
  mtime_unix_ns INTEGER NOT NULL,
  last_synced_at TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX synced_files_agent_session_idx ON synced_files(agent, session_id);
CREATE INDEX synced_files_source_path_idx ON synced_files(source_path);
CREATE INDEX synced_files_last_synced_at_idx ON synced_files(last_synced_at);
