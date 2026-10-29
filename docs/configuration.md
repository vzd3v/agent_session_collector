# Configuration

The collector uses TOML config files and rejects unknown keys. Keep one config
per role:

- sender host: `sender.toml`
- hub host: `hub.toml`

Examples:

- `examples/sender.example.toml`
- `examples/hub.example.toml`

## Sender Config

Top-level:

- `sender_id`: stable ID for this sender. Use the same value on the hub.

`[hub]`:

- `ssh_target`: SSH target used by rsync, for example
  `asc_upload_alpha@collector.example.net`.
- `remote_command`: command name expected on the hub. Usually `asc-receive`.

`[sync]`:

- `interval`: intended scheduler interval. The binary does not sleep on this;
  use it to keep config and scheduler documentation in sync.
- `state_path`: SQLite state file used to skip unchanged files.
- `rsync_path`: local rsync binary.
- `bandwidth_limit`: optional rsync bandwidth limit. Empty means unlimited.
- `max_attempts`: upload attempts per sync run.
- `backoff`: delay between failed upload attempts.

`[[sources]]`:

- `agent`: `claude` or `codex`.
- `user`: local operating-system user name used in mirror paths and events.
- `home`: absolute home directory for that user.
- `host_name`: stable host label for matching with telemetry.
- `env`: optional environment label, useful for Codex telemetry matching.
- `data_class`: named data class. The hub must allow this class.
- `paths`: relative transcript directories under `home`.
- `metadata`: optional bounded string map, for example `{ team = "platform" }`.
  Labels are retained in the catalog and archive snapshots. Do not put secrets in
  them. Duplicate `(user, agent)` source blocks are rejected.

`[sources.capture]` belongs to the immediately preceding source:

- `subagent_metadata`: default `true`; capture recognized Claude subagent
  `agent-*.meta.json` companions within configured paths.
- `session_index`: default `true`; capture the Codex global `session_index.jsonl`
  beside a single configured `sessions`/`archived_sessions` home. It is a name
  index artifact, not a conversation. Multiple inferred homes are rejected.
- `session_index_path`: optional explicit home-relative global index path when
  inference is unsuitable. Use the native `session_index.jsonl` basename.
- `tool_results`: default `false`; include files under `tool-results` directories
  reached through configured paths. Enabling it can materially increase raw data.

Regular `.jsonl` and native `.jsonl.zst` transcripts are collected without
requiring known record types or filenames. Excludes also apply to companions.
Source descriptors transfer automatically, including label-only updates; they
are not included in artifact upload counts (`metadata_uploaded` is separate).

`[exclude]`:

- `patterns`: relative glob patterns to exclude from scans.

`[logging]`:

- `format`: `json` or `text`.
- `level`: `debug`, `info`, `warn`, or `error`.

## Hub Config

Top-level:

- `hub_id`: stable ID for this hub. Data classes must explicitly allow it.

`[paths]`:

- `root`: common application root.
- `mirror`: raw sender mirror root.
- `archive`: compressed archive root.
- `catalog`: SQLite catalog path.
- `extracted`: reserved for future extracted/session-derived data.

All paths must be absolute. `mirror`, `archive`, `catalog`, and `extracted`
must live under `root`.

`[receive]`:

- `allow_delete`: whether receive-side rsync may delete remote mirror files.
  Keep this `false` unless you explicitly want sender-side deletion to remove
  hub mirror files.
- `max_file_size`: receive-side file-size cap.
- `unix_name_max_length`: validation limit for receive users and groups.

`[[receive.sender]]`:

- `sender_id`: sender config ID.
- `sender_slug`: filesystem-safe sender slug.
- `receive_user`: dedicated Unix account used for this sender's SSH key.
- `mirror_group`: group that grants write access to this sender's mirror.
- `mirror`: sender mirror directory name under `paths.mirror`.

`[data_classes.<name>]`:

- `raw_hub_allowed`: list of hub IDs allowed to store raw transcripts for this
  data class.
- `redaction_required`: currently must be `false`; redaction is not implemented.

`[index]`:

- `interval`: intended scheduler interval.
- `emit_vl_events`: emit `agent_session_index` JSON Lines to an HTTP endpoint.
- `vl_endpoint`: HTTP endpoint used when event emission is enabled.
- `emit_on_unchanged_every`: heartbeat interval for unchanged sessions.
- `max_decoded_file_size`: logical-byte limit for streaming identity projection,
  including native compressed input. Defaults to `receive.max_file_size` (2 GiB
  if unset). Corrupt or oversized input reports an artifact error while healthy
  artifacts continue; captured mirror bytes remain available for investigation.

The event endpoint is intentionally generic JSON Lines over HTTP. VictoriaLogs
is one compatible receiver; any endpoint accepting newline-delimited JSON can be
adapted.

`[archive]`:

- `enabled`: enable zstd compaction.
- `quiet_after`: minimum time since last mirror file change before archiving.
- `compression`: currently `zstd`.
- `zstd_level`: zstd compression level, `1` through `19`.
- `policy`: `replace` or `versioned`.
- `retain_plain_after_archive`: how long to keep the plain mirror file after a
  verified archive exists.
- `version_retention`: number of archive versions to keep when policy is
  `versioned`.
- `version_retention_days`: age window for archive versions when policy is
  `versioned`.

`[retention]`:

- `delete_mirror_after_archive`: delete plain mirror files after archive
  retention expires.
- `delete_unknown_after`: reserved for unknown-file cleanup; `0` disables it.
- `archive_retention_days`: reserved for archive expiry; `0` disables it.

## Safe Defaults

For most installations:

- run sender and hub through timers every `5m` to `15m`;
- keep `receive.allow_delete = false`;
- keep `archive.policy = "replace"` unless you need historical versions;
- keep `retention.delete_mirror_after_archive = true`;
- keep `archive.retain_plain_after_archive` between `24h` and `72h`.
