# agent_session_collector

`agent_session_collector` is a Go collector for Claude and Codex session JSONL
files. It is designed as timer-driven oneshot commands, not always-on daemons.

## Components

- `asc-sender`: scans configured transcript directories and uploads changed
  files with rsync.
- `asc-receive`: SSH forced-command gate for rsync receive in later phases.
- `asc-hub`: indexes, emits session index events, and compacts archives in later
  phases.

The sender sync command is implemented. Receive, hub indexing, and compaction
are implemented in later phases.

## Non-goals

- No transcript body analytics in the initial collector.
- No redaction or content substitution in the raw archive path.
- No always-on API or UI.
- No Python runtime dependency.
- No secrets or private deployment assumptions in this repository.

## Build

```bash
go test ./...
go build ./cmd/asc-sender ./cmd/asc-receive ./cmd/asc-hub
```

Static release build shape:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/asc-sender
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/asc-receive
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/asc-hub
```

## Configuration

Examples are in `examples/`. TOML parsing uses `github.com/BurntSushi/toml`
with strict unknown-key checks. The parser decision is recorded in
`docs/dev/decisions/2026-07-07-toml-parser.md`.

### Sender Sync

`asc-sender sync -config <path>` scans only `[[sources]].paths`, applies
`[exclude].patterns`, considers regular `.jsonl` files only, and uploads changed
files with rsync. The local SQLite state is `sync.state_path`; unchanged
`(path, size, mtime)` entries are skipped.

Use `asc-sender sync -config <path> -dry-run` to print the planned and skipped
files without running rsync or updating sync state.

## Data Contracts

Contracts for sender state, hub catalog, archive state, and VictoriaLogs index
events are in `docs/dev/data-contracts.md`.
