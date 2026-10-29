# Data Contracts

## Sender State

SQLite table `synced_files` records the last successfully uploaded identity for
each source file. The skip key is `(source_path, size_bytes, mtime_unix_ns)`;
`asc-sender sync` updates state only after successful upload.

Columns: `source_key`, `agent`, `user`, `source_path`, `session_id`,
`size_bytes`, `mtime_unix_ns`, `last_synced_at`, `last_error`.

Table `source_descriptors` separately tracks the canonical JSON SHA-256 for each
configured `(user, agent)` source. Label-only changes therefore upload without a
transcript change. Successful rsync is transport completion, not an acknowledgement
that the hub has indexed or archived the data.

## Source Descriptors

The authenticated sender's mirror contains `.asc/sources/<user>/<agent>.json`.
Version `1` carries `sender`, `user`, `agent`, `host_name`, `env`, `data_class`,
and custom `metadata` as a string map. The embedded identity must match the
receiver's configured sender and descriptor path. A source namespace may appear
only once in sender configuration. Metadata is limited to 32 fields, 64-byte keys,
1024-byte values, 16 KiB encoded map and 32 KiB total descriptor.

Hub table `sources` stores current descriptors, their hashes and observation time.
The source `updated_at` is the last descriptor-hash revision time, not a heartbeat.
If the descriptor file is absent, the last accepted descriptor is retained with
`source_metadata_provenance=last_accepted_descriptor`; a currently valid file uses
`present_descriptor`, and an unknown legacy source uses `unknown`. An invalid
present descriptor cannot silently fall back to accepted labels.
Source labels are sender assertions; SSH authenticates the sender, not an asserted
operating-system user or hostname. Descriptors cannot override sender isolation.
The hub validates declared data classes against its own raw-storage policy while
indexing; this is not pre-transfer classification or content validation.
Invalid or unsupported descriptors report an error for that source while healthy
sources continue. A legacy source without a descriptor remains collectable with
unknown labels.

## Hub Catalog

SQLite table `sessions` stores one current row per collected artifact, including
child transcripts and files with unresolved vendor identity.

Identity fields: `session_key`, `agent`, `sender`, `user`, `env`, `host_name`,
`session_id`, `conversation_id`.

`source_id` hashes the explicit tuple `(sender, user, agent)`. `artifact_id` hashes
`(source_id, relative_path)` using JSON tuple encoding and SHA-256; it survives hub
root relocation. Neither is a vendor conversation ID. `relative_path` is relative
to the sender's user/agent namespace. Legacy `session_key` and `session_id` remain
compatible storage keys; new consumers must use `artifact_id` and native fields.
The lookup on `(agent, sender, user, session_id)` remains nonunique.

Artifact roles are `transcript`, `subagent_metadata`, `session_name_index`, and
`tool_result`. The Codex global name index has no session identity. Claude child
filenames and containing UUID directories establish agent/root evidence; a root
does not prove a direct parent. Codex `session_meta.id` establishes the native
thread. A matching filename header selects the owner in inherited history; a sole
distinct header also covers renamed/reverted rollouts. Multiple unmatched headers
remain unresolved. Explicit native parent/fork fields provide separate edges.
Unknown records and incomplete tails remain in the captured bytes.

Native fields are `native_session_id`, `native_agent_id`, `native_root_id`,
`native_parent_id`, `native_fork_id`. `identity_evidence_json` records projection
status and evidence. `projection_version` and `projection_raw_sha256` cache that
rebuildable projection by parser revision and logical hash. Existing archive-only
rows are backfilled by streaming the verified archive once when this cache is
stale; no new developer session is necessary. This is an identity projection,
not a general transcript reader or an executable replay facility.

File fields: `mirror_path`, `latest_size_bytes`, `latest_mtime_unix_ns`,
`latest_raw_sha256`, `archive_status`, `latest_archive_path`,
`latest_archive_sha256`.

`source_encoding` is `plain` or `zstd`. `latest_size_bytes` and
`latest_physical_sha256` describe original physical source bytes;
`latest_raw_size_bytes` and `latest_raw_sha256` describe decoded logical bytes.
Native `.jsonl.zst` is transported unchanged and decoded as a stream for hashing
and identity evidence. Compaction verifies both representations and copies native
zstd bytes without double compression. Metadata and role are independent of
whether the artifact is a conversation.

`host_name`, `env`, `data_class`, `metadata_json`, `metadata_sha256` contain current
source metadata, including for archive-only records. These updates do not change
the content clock. `last_index_event_metadata_sha256` supports retrying metadata
event delivery independently of content changes.

Indexing persists the catalog before sending optional log events. A failed HTTP
request cannot block capture/catalog progress. `last_index_event_raw_sha256` and
`last_index_event_metadata_sha256` acknowledge only successful event transmission;
pending content/metadata is retried before the heartbeat interval without
renewing the content clock. Delivery is at least once; partial HTTP consumption
or a crash between delivery and acknowledgement can produce duplicate events.

Timeline fields: `first_seen_at`, `last_indexed_at`, `last_changed_at`,
`last_index_event_at`. Indexing/heartbeat advances only `last_indexed_at` and
event time. `last_changed_at` advances only when the logical content hash changes.
It is observation time, not sender receipt time or developer activity. Migrated
rows have unknown (`''`) change time until a real change is observed.

## Archive State

SQLite table `session_archives` stores archive versions. Version `0` is the
current archive for replace policy. Versioned policy starts at `1`.

Columns: `session_key`, `version`, `path`, `raw_size_bytes`,
`raw_mtime_unix_ns`, `raw_sha256`, `archive_sha256`, `created_at`,
`superseded_at`.

Each archive has an adjacent `<archive-path>.metadata.json` version-1 snapshot
containing portable source/artifact identity, role, original relative path,
physical/logical sizes and hashes, native evidence, and source labels. New
snapshots have `provenance=archive_time`; missing historical snapshots can be
backfilled with `provenance=legacy_backfill_current_metadata` and the actual
capture time. Backfill never claims that today's labels were known at archive
creation. Existing snapshots are immutable when current source labels change.
Compact also repairs missing verified current-generation snapshots without a
plain mirror, before resuming backed deletions. It does not invent snapshots for
superseded historical versions.
Legacy rows without portable identity and a projection of the current digest
defer publication until indexing. Compaction protects these rows rather than
freezing empty IDs into an immutable snapshot; unresolved native IDs remain valid.
Keep these sidecars with their archives when moving/exporting them. They are
metadata, not complete tool-output dependencies or execution environments.

## Lifecycle

Index, compact and migrate serialize through `<catalog>.lock` using Linux flock.
`asc-hub migrate` applies only schema migrations and prints `migrate applied=N`;
a repeat reports `0`. Stop/drain scheduled writers before binary/config/schema
upgrades and take a SQLite backup. Migrations preserve existing artifact keys.

`pending_deletions` persists root-relative paths, expected physical SHA-256/size,
reason, grace deadline and a deterministic quarantine name. Archive publication
syncs the file/directory before committing its catalog pointer. Destructive
transactions use SQLite synchronous FULL and commit the deletion intent before
filesystem removal. Replacement metadata is published and synced before its
current pointer and retirement intents commit. Replace retires the old
archive/sidecar with that transaction. Version pruning runs independently of
new compression. Every actual backed deletion (plain mirror or superseded
archive) revalidates its backing archive and snapshot, including during recovery;
missing/corrupt backing data preserves/restores the selected copy and reports an
error.

Deletion renames into hub-owned `0700` `.asc-trash` under the corresponding
storage root, rechecks the quarantined generation, unlinks and syncs directories,
then clears the intent. This allows retry after process interruption at each
stage. A mismatching live generation is kept. A race detected after rename is
restored without overwriting a newer arrival; if restoration conflicts, both
files are preserved and compact reports the quarantine for operator review.
Keep storage roots and quarantines on the same filesystem. Receivers cannot
write quarantine, catalog or archive directories.

Group expiry first persists `pending_expiry_groups` and blocked mirror intents,
then reserves all selected mirror generations in private quarantine. Changed
members or observed unindexed source artifacts abort the group and restore its
members without changing catalog/archive state. Precommit crash recovery rolls
the reservation back; a conflicting new arrival is preserved with the quarantine
and reported for operator review. Blocked intents cannot be drained as deletions.

The reservation epoch selects known old generations. Sequential generation and
namespace checks do not exclude receivers or form an atomic filesystem snapshot.
Conflicts observed before commit abort expiry; unobserved publications after
reservation are later generations, retained for reindexing, and cannot
retroactively undo expiry. No receiver admission protocol is introduced.

A successful reservation commits digest-specific `expiry_tombstones`, archive
intents, promotion of blocked mirror intents and catalog removal in one
transaction. Recovery then finishes only the reserved old generations. Indexing
rejects identical expired content even
when a sender reuploads it; different content at the same artifact path is
accepted. Tombstones are retained without a time limit because sender source
files are not deleted. Their size grows with expired artifact generations.
Replay protection is eventual hub indexing/cleanup, not transport admission or
a sender receipt. A valid same-content replay may occupy mirror space until the
next compact. Source cleanup and archival acknowledgements are outside this
contract.

TTL uses `last_changed_at` (logical-hash changes only), falling back to valid
legacy `first_seen_at`. It does not use source mtime, labels or heartbeat time.
Known native roots, repeated thread pages and same-source explicit Codex parent
edges form bounded groups; explicit fork edges do not. An incomplete identity
graph uses only available evidence. Global name indexes are source-scoped shared
artifacts, and unknown tool-result ownership is protected. Maintenance targets
include every physical regular file below the archive root. Unknown files and
graces can prevent reaching a target; this is reported without overriding safety.

The preview uses one read-only SQLite transaction. It verifies existing data and
reports reasons/physical bytes without recovering intents or changing storage.
It is a catalog snapshot, not a reservation against concurrent receiver writes.

## VictoriaLogs Event

Event name: `agent_session_index`.

Required fields: `event.schema_version` (`2`), `event.name`, `service.name`,
`agent`, `artifact.id`, `source.id`, `artifact.role`,
`file.size_bytes`, `file.mtime`, `archive.status`, `sync.status`,
`collector.run_id`.

Additional matching fields: `conversation.id`, `host.name`, `os.user`, `env`.

Version 2 emits `session.id` only for resolved native identities; legacy event
consumers must not treat it as the artifact key. For resolved Codex events,
`conversation.id` equals the native `session.id`; unresolved artifacts omit both.
Native agent/root/parent/fork fields are optional. Claude explicit parent agent
IDs use `agent.parent_id`; Codex parent thread IDs use `session.parent_id`.
The catalog's `native_parent_id` is interpreted in the corresponding agent's
native namespace. Custom source labels and data
class remain in the catalog and archive snapshots, not exported to the log
endpoint by default. `file.changed` distinguishes a changed capture from a repeat
index heartbeat; `file.changed_at` and `index.observed_at` separate their clocks.
`archive.status` reports the effective catalog state for that capture; an
unchanged archived artifact stays archived in heartbeat events.

The event must not include transcript text, tool output, credentials, or raw
user-home paths.
