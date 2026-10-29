# Security Model

`agent_session_collector` moves raw AI-agent transcripts. Those files may contain
credentials, source code, customer data, prompts, model responses, and command
output. Deploy it only on infrastructure that is allowed to store that data.

## Trust Boundaries

- Sender hosts are trusted to read local transcript directories.
- The hub is trusted to store raw transcript mirrors, compressed archives, and
  session-index metadata.
- The collector does not redact or classify transcript content.
- A log endpoint configured through `index.vl_endpoint` receives session-index
  metadata, not raw transcript bodies.

## Receive Isolation

Use one dedicated Unix receive account per sender. In `authorized_keys`, bind
the sender key to `asc-receive` with an SSH forced command:

```text
command="/usr/local/bin/asc-receive -config /etc/agent-session-collector/receive.toml -sender sender-alpha",restrict ssh-ed25519 AAAA... sender-alpha
```

The forced command validates:

- the configured sender ID;
- the local Unix receive user and mirror group;
- the rsync server-mode command requested by SSH.

Keep private SSH keys on sender hosts only.

## File Permissions

Recommended ownership:

- hub config: readable by the hub job user, not world-readable; receive accounts
  use a separate receiver-readable config without backend credentials;
- mirror directory: writable by its sender receive account and the hub job
  user, with access/default ACLs covering new subdirectories;
- archive and catalog directories: writable only by the hub job user;
- sender state database and SSH key: readable only by the sender job user.

Receive accounts need an executable login shell because sshd invokes forced
commands with `shell -c`. Block unrestricted login through dedicated forced-command
keys, disabled password authentication and no other authorized keys. Do not use
`nologin`, grant the shared hub group, or reuse these accounts for other automation.
See [the deployment ACL recipe](deployment.md#receive-accounts).

## Deletion and Retention

Keep `receive.allow_delete = false` unless sender-side deletion must remove hub
mirror files. With the default archive policy, the hub compacts quiet sessions to
`.jsonl.zst` and may delete the plain mirror copy after
`archive.retain_plain_after_archive`.

## Incident Response

If a sender key is exposed:

1. Remove that key from the receive account's `authorized_keys`.
2. Rotate the sender key.
3. Inspect the sender's mirror directory for unexpected files.
4. Run `asc-hub doctor` and `asc-hub index` after restoring the account.
