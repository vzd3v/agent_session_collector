# 2026-07-07: TOML Parser

Status: accepted

Context: operator config needs comments, arrays of sources/senders, duration
strings, strict unknown-key detection, and a small dependency surface.

Decision: use `github.com/BurntSushi/toml v1.6.0` with strict unknown-key
checks.

Alternatives: `github.com/pelletier/go-toml/v2 v2.4.3` is active and MIT, but
has a larger import surface for this use case. Hand parsing was rejected because
TOML arrays and strict diagnostics are better handled by a parser.

Consequences:

- Config parsing stays small and deterministic.
- Unknown-key diagnostics remain part of the config contract.
- Dependency freshness is verified by routine `go list -m -u all` and
  `govulncheck ./...` checks, not by hard-coded "latest" claims in docs.
