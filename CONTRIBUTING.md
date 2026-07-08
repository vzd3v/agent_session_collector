# Contributing

Thanks for improving `agent_session_collector`.

## Development Setup

Requirements:

- Go 1.26.4 or newer
- `rsync` for receive/sender integration tests
- `zstd` for archive tests and local compaction

Run the standard checks:

```bash
make test
make lint
make vulncheck
```

`make lint` currently runs `go vet ./...`. If `golangci-lint` is installed, run
it before larger pull requests.

## Pull Requests

- Keep examples synthetic and portable.
- Do not commit real transcripts, secrets, hostnames, usernames, or private
  deployment details.
- Update docs when changing CLI behavior, config fields, archive behavior, or
  emitted event contracts.
- Add or update focused tests for behavior changes.

## Compatibility

The project is pre-1.0. Config shape, emitted metadata, and internal package
layout may change, but changes should be documented in the same pull request.
