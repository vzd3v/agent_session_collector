# Agent Instructions

This repository is the `agent_session_collector` Go application.

- Follow `vz-general-rules` and `vz-go`.
- Keep examples synthetic and neutral: no private hostnames, users, paths, tokens,
  or copied transcript content.
- Architecture foundations: internal corporate tool, single hub per deployment,
  systemd timer/oneshot execution by default, SQLite-owned state, no resident
  sender or hub process between timer runs.
- Persistent contracts live in `docs/dev/data-contracts.md`.
- Tests are stdlib `testing`; schema tests use `modernc.org/sqlite`.
