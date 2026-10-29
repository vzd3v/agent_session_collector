ALTER TABLE sessions RENAME COLUMN last_seen_at TO last_indexed_at;
ALTER TABLE sessions ADD COLUMN last_changed_at TEXT NOT NULL DEFAULT '';
DROP INDEX sessions_last_seen_at_idx;
CREATE INDEX sessions_last_indexed_at_idx ON sessions(last_indexed_at);
CREATE INDEX sessions_last_changed_at_idx ON sessions(last_changed_at);
