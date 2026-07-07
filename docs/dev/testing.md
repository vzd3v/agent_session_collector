# Testing

Run:

```bash
go test ./...
```

Current Phase 1 coverage:

- config validation and example parsing
- session ID extraction
- SQLite schema application on empty and existing databases
- synthetic fixture privacy markers
- sender scan, exclude, upload planning, dry-run, retry/state, and staging
  safety behavior

Fixtures under `testdata/fixtures` are generated JSONL only. Tests fail if they
contain known private deployment markers or omit `ASC_SYNTHETIC_FIXTURE`.
