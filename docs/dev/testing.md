# Testing

Run:

```bash
go test ./...
```

Current coverage:

- config validation and example parsing
- session ID extraction
- SQLite schema application on empty and existing databases
- synthetic fixture privacy markers
- sender scan, exclude, upload planning, dry-run, retry/state, and staging
  safety behavior
- receive forced-command parsing, rsync server-mode allowlist, mirror path
  confinement, and sender-profile compatibility
- hub indexing, VL event retry/idempotency, zstd compaction, replace/versioned
  archive policies, checksum mismatch guards, and mirror deletion retention
- opt-in grouped TTL/byte pressure, explicit Codex parent/page versus fork edges,
  expired-content replay, current-only replacement, inactive version maintenance,
  orphan discovery grace, interrupted deletion recovery, and read-only preview
- bounded staging batches including an oversized single artifact

Fixtures under `testdata/fixtures` are generated JSONL only. Tests fail if they
contain known private deployment markers or omit `ASC_SYNTHETIC_FIXTURE`.
