# Security Policy

## Supported Versions

`agent_session_collector` is pre-1.0. Security fixes are applied to the default
branch until versioned releases are published.

## Reporting a Vulnerability

Do not open a public issue for a vulnerability that exposes transcript data,
credentials, receive-account bypasses, or archive integrity problems.

Report privately through GitHub Security Advisories for this repository. Include:

- affected version or commit;
- deployment mode;
- reproduction steps;
- expected impact;
- whether transcript content, credentials, or SSH keys may be exposed.

## Sensitive Data

This project intentionally collects raw AI-agent transcripts. It does not redact
or sanitize transcript bodies. Operators are responsible for deploying it only on
systems that are allowed to store those transcripts.
