# agent_session_collector

`agent_session_collector` collects local AI-agent session transcripts from
workstations or servers, mirrors them to a central host, indexes them, and
optionally archives quiet sessions with zstd.

It is built for fleets where Claude Code and OpenAI Codex CLI are used on many
hosts and transcript files need to be available for later audit, debugging, or
offline analysis. The default operating model is timer-driven oneshot commands:
no sender daemon, no resident hub process, and no database server.

## What It Does

- Scans Claude/Codex `.jsonl` and native `.jsonl.zst` artifacts, with default
  subagent metadata/global name-index capture and optional tool-result files.
- Uploads changed files to a hub with rsync over SSH.
- Restricts hub-side rsync through an `asc-receive` forced command.
- Maintains a SQLite catalog of sessions and archive state.
- Emits lightweight `agent_session_index` JSON Lines to an HTTP log endpoint.
- Compresses quiet sessions to `.jsonl.zst` with replace or versioned archive
  policy.

## What It Does Not Do

- It does not redact transcript content.
- It does not analyze prompts, tool output, or model responses.
- It does not expose an API or UI.
- It does not require Python, Postgres, Redis, or a queue.

Transcripts can contain credentials, source code, customer data, and personal
data. Treat the hub as sensitive infrastructure. See
[docs/security-model.md](docs/security-model.md) before deploying.

## Components

| Binary | Runs on | Purpose |
| --- | --- | --- |
| `asc-sender` | sender hosts | Scan transcript files, skip unchanged files, upload changed snapshots with rsync. |
| `asc-receive` | hub host | SSH forced-command wrapper that validates sender identity and rsync server mode. |
| `asc-hub` | hub host | Index mirror files, emit session-index events, compact quiet files, run health checks. |

## Requirements

- Go 1.27.3 or newer to build.
- `rsync` on sender and hub hosts.
- `zstd` on the hub when archive compaction is enabled.
- SSH access from each sender to its dedicated hub receive account.

Runtime state is stored in SQLite files:

- sender: `sync.state_path`
- hub: `paths.catalog`

## Quick Start

Build the binaries:

```bash
make build
```

Copy and edit the example configs:

```bash
cp examples/hub.example.toml /etc/agent-session-collector/hub.toml
cp examples/sender.example.toml /etc/agent-session-collector/sender.toml
```

Validate the hub host:

```bash
asc-hub doctor -config /etc/agent-session-collector/hub.toml
```

Preview sender uploads without changing state:

```bash
asc-sender sync -config /etc/agent-session-collector/sender.toml -dry-run
```

Run one sender sync:

```bash
asc-sender sync -config /etc/agent-session-collector/sender.toml
```

Run one hub index and compaction pass:

```bash
asc-hub index -config /etc/agent-session-collector/hub.toml
asc-hub compact -config /etc/agent-session-collector/hub.toml
```

For production, run these commands from systemd timers or your scheduler of
choice. See [docs/deployment.md](docs/deployment.md).

## Configuration

Configuration is TOML with strict unknown-key checks. See:

- [examples/sender.example.toml](examples/sender.example.toml)
- [examples/hub.example.toml](examples/hub.example.toml)
- [docs/configuration.md](docs/configuration.md)

Durations use Go syntax such as `10m`, `24h`, and `0`. Byte sizes accept `B`,
`KiB`, `MiB`, and `GiB`.

## Session Identity

Every artifact has a portable ID derived from its sender/user/agent source and
relative path. Native session, agent, root, parent and fork IDs are separate,
evidence-based fields. Codex inherited history is not assigned to the first
header blindly; ambiguous ownership stays unresolved. Claude root-directory
evidence does not imply a direct parent. Unknown records and filenames remain
collectable.

Source descriptors preserve configured hostname, user, environment, data class
and bounded custom labels, including label-only updates for archived artifacts.
Version-2 index events always carry artifact/source IDs and omit unresolved
native session IDs. Content age changes only with the logical hash; a heartbeat
or label edit is not new content. See
[docs/dev/data-contracts.md](docs/dev/data-contracts.md).

## Archive Behavior

The hub indexes mirror files first. A separate compaction pass archives sessions
that have been quiet longer than `archive.quiet_after`.

Recommended defaults:

- `archive.compression = "zstd"`
- `archive.zstd_level = 6`
- `archive.policy = "replace"`
- `retention.delete_mirror_after_archive = true`
- `archive.retain_plain_after_archive = "48h"`

This keeps recent quiet sessions readable as plain JSONL for a short window and
then leaves a verified `.jsonl.zst` archive.

## Development

```bash
make test
make lint
make vulncheck
```

Synthetic fixtures live under `testdata/fixtures`. They must not contain real
transcripts or private deployment markers.

More details:

- [CONTRIBUTING.md](CONTRIBUTING.md)
- [docs/dev/testing.md](docs/dev/testing.md)
- [docs/dev/data-contracts.md](docs/dev/data-contracts.md)

## License

MIT. See [LICENSE](LICENSE).
