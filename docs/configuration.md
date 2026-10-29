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
- `max_staging_bytes`: batch target; absent/`0` means `512MiB`. Each batch is
  removed after its rsync. An individual larger artifact travels alone, so peak
  staged payload is `max(target, largest artifact)` plus list/directory overhead.
  Files growing after scan are skipped for a later run. A start-time free-space
  check precedes staging; it is not a disk reservation.
  The target covers an active batch. SIGKILL/power loss can leave private staging
  directories behind; use OS temporary-directory cleanup or a service-managed
  ephemeral `TMPDIR`. The collector does not sweep other processes' temp files.

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
  `replace` keeps only the verified current generation, including after switching
  from `versioned`; superseded legacy rows/files are retired regardless of the
  version-retention knobs. The current legacy version number is preserved until
  a new version-0 replacement is published.
- `retain_plain_after_archive`: how long to keep the plain mirror file after a
  verified archive exists.
- `version_retention`: number of archive versions to keep when policy is
  `versioned`.
- `version_retention_days`: age window for archive versions when policy is
  `versioned`. Count and age protections are combined; the current archive is
  always kept. Legacy versions are pruned on every compact pass, including when
  there are no newly archived sessions.

`[retention]`:

- `delete_mirror_after_archive`: delete plain mirror files after archive
  grace expires, after verifying the compressed physical/logical hashes and a
  valid metadata snapshot. A missing/corrupt archive preserves the plain copy.
- `archive_max_age`: maximum age since hub-observed logical content change;
  `0` disables expiry. Durations use Go syntax (`8760h`, not `365d`). Valid
  `first_seen_at` is the conservative legacy fallback; unknown clocks protect data.
- `archive_max_bytes`: archive maintenance target, including sidecars, pending
  files and unmanaged files; `0` disables it. Eligible oldest complete groups
  expire until the target is reached. This is not an ingress quota.
- `min_free_bytes`: filesystem free-space floor; `0` disables it. Checked before
  receiver rsync starts and before archive compression/copy, using the actual
  destination filesystem. Concurrent writes can cross the floor. Configure OS
  quotas/reserved space when a hard admission boundary is required.
- `orphan_grace`: when nonzero, first discovery of an unreferenced recognized
  archive/temp/sidecar schedules cleanup after this grace. Source-preserved mtime
  never shortens it. `0` disables orphan discovery. Unrecognized files are kept.
- `delete_unknown_after`, `archive_retention_days`: reserved compatibility keys;
  only `0` is accepted. They have no deletion semantics.

All new destructive age/size/orphan limits are disabled by default. Current
replacement objects are retired automatically; no historical versions accumulate
under `replace`. Active, unarchived, unverified, unknown-clock and plain-grace
groups remain protected. Unsatisfied targets report `pressure_unsatisfied` and
exit nonzero instead of deleting protected data.
Free-space cleanup selects only groups releasing bytes on a constrained
filesystem; pressure on a separate mirror filesystem cannot expire archive-only
groups. Unindexed source artifacts protect that source until indexing catches up.

Retention groups share a source namespace and explicit native root/parent identity;
repeated Codex thread pages are kept together. Fork ancestry remains separate.
Claude tool-result ownership requires the exact root directory and a matching
cataloged native root; unknown ownership stays protected. The global Codex name
index is stored once and kept while any source artifact remains. Its content
does not refresh transcript TTL. See [the lifecycle contract](dev/data-contracts.md#lifecycle).

Preview configured retention before enabling a policy:

```bash
asc-hub migrate -config /etc/agent-session-collector/hub.toml
asc-hub compact -config /etc/agent-session-collector/hub.toml -dry-run
```

Preview returns JSON actions, protected reasons, physical archive bytes and
projected remaining pressure. It requires the current catalog schema and opens
SQLite with `mode=ro`: no migrations, compression, sidecars or cleanup occur.
SQLite may create an empty `-wal` and update transient reader marks in `-shm`;
catalog content, WAL frames and artifacts stay unchanged. The filesystem can
advance during the preview.

## Safe Defaults

For most installations:

- run sender and hub through timers every `5m` to `15m`;
- keep `receive.allow_delete = false`;
- keep `archive.policy = "replace"` unless you need historical versions;
- keep `retention.delete_mirror_after_archive = true`;
- keep `archive.retain_plain_after_archive` between `24h` and `72h`.
