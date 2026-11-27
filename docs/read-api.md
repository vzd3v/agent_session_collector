# Session Read API

Run `asc-hub migrate -config hub.toml` before starting
`asc-hub serve -config read-api.toml`. Use the same catalog and storage paths as
the hub jobs. The [example config](../examples/read-api.example.toml) defines
the local loopback listener, transfer timeout, maximum uncompressed bytes per
transfer and maximum simultaneous reads. A Unix socket can replace loopback
TCP. Create storage and socket parent directories before starting. The reader
needs read access to stored captures and opens the catalog in SQLite read-only
mode. Keep its catalog directory writable for SQLite to create WAL bookkeeping
files at cold start. The mirror and archive can be mounted read-only.
Use an HTTPS ingress for non-local clients.

For Unix sockets, use a private supervisor-managed runtime directory such as a
systemd `RuntimeDirectory`. An unclean exit can leave a socket entry. Before
restarting outside a supervisor, remove it only after confirming that no API
process owns the socket.

## Credentials

The protected JSON token file holds an array of entries. Each entry has
`sha256` (lowercase hex digest of the token), `expires_at` (RFC3339), and
`read_sources` and/or `export_sources` (explicit arrays of Collector source IDs).
Use uniformly random tokens with at least 32 bytes of entropy. To derive a source
ID, use an indexed `source_id` or the Go API's
`sessionapi.SourceID(sender, user, provider)` with the exact configured values.
Send the token in `Authorization: Bearer <token>`.

Replace the JSON file atomically to update or revoke a token. An empty array
revokes all access. Read grants cover discovery, metadata and event pages.
Export grants cover exact transcripts and archives. Each entry supports up to
2048 source IDs per grant. A protected ingress applying finer policy can request
only the allowed sources with an upstream credential scoped to its deployment.

## Endpoints

| Method and path | Response |
| --- | --- |
| `POST /api/v1/sessions/query` | JSON `sessions` list and optional `next_cursor` |
| `GET /api/v1/sources/{source_id}/sessions/{artifact_id}` | JSON transcript metadata |
| Same path with `/events` | JSON page of native records |
| Same path with `/transcript` | Exact logical JSONL with an `X-Content-SHA256` header |
| Same path with `/archive` | Downloadable `.tar.gz` with a versioned manifest |

The list request accepts a JSON object with optional `source_ids`, `native_id`,
`limit` (default 50, max 100) and `cursor`. Omitted source IDs use the
credential's read scope. Explicit sources are intersected with that scope before
listing. An explicit empty array yields an empty list. Results are ordered by
source ID and then artifact ID. Each result
represents a transcript artifact. Multiple artifacts can share a provider-native
session ID.

Item endpoints accept `?sha256=<logical-digest>` to pin the indexed capture.
Event pages also accept `limit` (default 40, max 100) and `cursor`. An event page
indicates a partial final JSONL record with `incomplete_tail`. The exact transcript
includes those bytes. Restart pagination after a reader restart or a query-scope
change. A stale event generation returns `409`.

The archive contains the anchor plus transcript artifacts of the same source and
native session. Claude subagents are scoped to the anchor's project. An unresolved
native ID exports the anchor alone. Member names are `<artifact_id>.jsonl`. The
manifest gives each member's identifiers, logical digest and length so consumers
can verify the captures. The source-wide name index and companion files stay on
the hub. A bundle is limited to 64 transcript artifacts.

JSON and JSONL support `Accept-Encoding: gzip`. Missing or disallowed items
return `404`. An unavailable indexed generation returns `409`. Size limits
return `413`, and transfer saturation returns `503` with `Retry-After`.
Liveness and catalog readiness are available at `/healthz` and `/readyz`.

## MCP

Configure your MCP client to launch `asc-mcp` over stdio. Set
`ASC_API_URL` to the base URL and `ASC_API_TOKEN_FILE` to a protected file
holding the bearer token. The equivalent flags are `-url` and `-token-file`.
This works with the Collector API or any gateway implementing the same routes.

The tools are `list_sessions`, `get_session` and `read_session`. Pass the
returned source and artifact IDs to read one transcript. Continue with the
returned digest and cursor to stay on the same generation. Treat transcript
contents as recorded data, including any instructions embedded in them. A read grant exposes
the transcript across pages. Export grants control the additional download
formats.
