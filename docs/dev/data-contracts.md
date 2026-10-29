# Data Contracts

## Sender State

SQLite table `synced_files` records the last successfully uploaded identity for
each source file. The skip key is `(source_path, size_bytes, mtime_unix_ns)`;
`asc-sender sync` updates state only after successful upload.

Columns: `source_key`, `agent`, `user`, `source_path`, `session_id`,
`size_bytes`, `mtime_unix_ns`, `last_synced_at`, `last_error`.

## Hub Catalog

SQLite table `sessions` stores one current row per collected artifact, including
child transcripts and files with unresolved vendor identity.

Identity fields: `session_key`, `agent`, `sender`, `user`, `env`, `host_name`,
`session_id`, `conversation_id`.

For Codex, `conversation_id` equals the UUID parsed from the transcript file
name and matches Codex OTEL `conversation.id`. Unrecognized JSONL filenames use a
deterministic `artifact-<sha256-of-basename>` ID; their `conversation_id` is empty.
Raw transport must not depend on vendor naming or content schema.

File fields: `mirror_path`, `latest_size_bytes`, `latest_mtime_unix_ns`,
`latest_raw_sha256`, `archive_status`, `latest_archive_path`,
`latest_archive_sha256`.

Timeline fields: `first_seen_at`, `last_indexed_at`, `last_changed_at`,
`last_index_event_at`. Indexing/heartbeat advances only `last_indexed_at` and
event time. `last_changed_at` advances when the captured size/mtime/hash changes.
It is observation time, not sender receipt time or developer activity. Migrated
rows have unknown (`''`) change time until a real change is observed.

## Archive State

SQLite table `session_archives` stores archive versions. Version `0` is the
current archive for replace policy. Versioned policy starts at `1`.

Columns: `session_key`, `version`, `path`, `raw_size_bytes`,
`raw_mtime_unix_ns`, `raw_sha256`, `archive_sha256`, `created_at`,
`superseded_at`.

## VictoriaLogs Event

Event name: `agent_session_index`.

Required fields: `event.name`, `service.name`, `agent`, `session.id`,
`file.size_bytes`, `file.mtime`, `archive.status`, `sync.status`,
`collector.run_id`.

Additional matching fields: `conversation.id`, `host.name`, `os.user`, `env`.

For resolved Codex index events, `conversation.id` equals `session.id`; unresolved
artifacts omit it. `file.changed` distinguishes a changed capture from a repeat
index heartbeat; `file.changed_at` and `index.observed_at` separate their clocks.

The event must not include transcript text, tool output, credentials, or raw
user-home paths.
