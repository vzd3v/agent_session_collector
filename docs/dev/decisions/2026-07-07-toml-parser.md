# 2026-07-07: TOML Parser

Status: accepted

Context: operator config needs comments, arrays of sources/senders, duration
strings, strict unknown-key detection, and a small dependency surface.

Decision: use `github.com/BurntSushi/toml v1.6.0` with strict unknown-key
checks.

Alternatives: `github.com/pelletier/go-toml/v2 v2.4.3` is active and MIT, but
has a larger import surface for this use case. Hand parsing was rejected because
TOML arrays and strict diagnostics are better handled by a parser.

Health check on 2026-07-07:

- `github.com/BurntSushi/toml`: latest `v1.6.0`, released 2025-12-18, MIT,
  stable v1 module, TOML 1.1 compatible, no transitive module dependencies
  after `go mod tidy`.
- `github.com/pelletier/go-toml/v2`: latest `v2.4.3`, released 2026-07-05, MIT,
  active, but more imports than needed here.
- Advisory check: `govulncheck ./...` is part of the local verification gate for
  this phase.

Sources checked: `pkg.go.dev`, GitHub releases, GitLab project metadata for
`modernc.org/sqlite`, and `go list -m -json`.
