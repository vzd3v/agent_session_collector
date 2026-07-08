# Agent Instructions

This repository contains the `agent_session_collector` Go application.

## Scope

- Keep the repository suitable for public release.
- Do not commit real transcripts, hostnames, usernames, tokens, private keys, or
  deployment-specific infrastructure details.
- Keep examples synthetic and portable.
- Prefer small, explicit changes over broad rewrites.

## Architecture

- One hub can receive from many sender hosts.
- Senders and hub jobs are designed for scheduled oneshot execution.
- Runtime state is local SQLite; do not add a database server, queue, or daemon
  unless the product requirements change.
- Persistent data contracts live in `docs/dev/data-contracts.md`.

## Go

- Run `gofmt` on modified Go files.
- Keep packages focused around the entity they manage.
- Do not introduce global mutable state for command behavior.
- Tests use the standard `testing` package; SQLite tests use
  `modernc.org/sqlite`.

## Docs

- User-facing setup belongs in `README.md`, `docs/deployment.md`, and
  `docs/configuration.md`.
- Security-sensitive behavior belongs in `docs/security-model.md` and
  `SECURITY.md`.
- Development-only design notes belong under `docs/dev/`.
