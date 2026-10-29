CREATE TABLE source_descriptors (
  source_key TEXT PRIMARY KEY,
  sha256 TEXT NOT NULL,
  last_synced_at TEXT NOT NULL
);
