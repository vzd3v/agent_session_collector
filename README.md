# agent_session_collector

`agent_session_collector` is a Go collector for Claude and Codex session JSONL
files. It is designed as timer-driven oneshot commands, not always-on daemons.

## Components

- `asc-sender`: scans configured transcript directories and uploads changed
  files in later phases.
- `asc-receive`: SSH forced-command gate for rsync receive in later phases.
- `asc-hub`: indexes, emits session index events, and compacts archives in later
  phases.

Phase 1 contains command skeletons, config contracts, SQLite schemas, data
contracts, synthetic fixtures, and tests. Scanner, receive, indexer, and compact
behavior are intentionally not implemented yet.

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

## Data Contracts

Contracts for sender state, hub catalog, archive state, and VictoriaLogs index
events are in `docs/dev/data-contracts.md`.
