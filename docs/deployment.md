# Deployment

This guide describes a minimal Linux deployment with one central hub and one or
more sender hosts.

## Build

Use Go 1.27.3 or newer; run `make test lint vulncheck` before building.
Install `rsync`, `zstd`, OpenSSH and `setfacl` (Debian/Ubuntu package `acl`).

```bash
make build
```

Install binaries on the hub:

```bash
install -m 0755 dist/asc-hub /usr/local/bin/asc-hub
install -m 0755 dist/asc-receive /usr/local/bin/asc-receive
```

Install the sender binary on each sender host:

```bash
install -m 0755 dist/asc-sender /usr/local/bin/asc-sender
```

## Hub Layout

Create an application root owned by a dedicated hub user:

```bash
useradd --system --user-group --home /var/lib/agent-session-collector --shell /usr/sbin/nologin asc-hub
install -d -o asc-hub -g asc-hub -m 0750 /var/lib/agent-session-collector
install -d -o asc-hub -g asc-hub -m 0750 /var/lib/agent-session-collector/{mirror,archive,catalog,extracted}
install -d -o root -g asc-hub -m 0750 /etc/agent-session-collector
```

Copy and edit `examples/hub.example.toml`:

```bash
install -m 0640 -o root -g asc-hub examples/hub.example.toml /etc/agent-session-collector/hub.toml
```

Run:

```bash
sudo -u asc-hub asc-hub doctor -config /etc/agent-session-collector/hub.toml
```

## Receive Accounts

Create one Unix receive account per sender. Each account should be able to write
only to its configured mirror directory.

Example for `sender-alpha`:

```bash
groupadd --system asc_mirror_alpha
useradd --system --user-group --home /var/lib/agent-session-collector/receive/sender-alpha \
  --shell /bin/sh --groups asc_mirror_alpha asc_upload_alpha

install -d -o asc_upload_alpha -g asc_mirror_alpha -m 0770 \
  /var/lib/agent-session-collector/mirror/sender-alpha
install -d -o root -g root -m 0700 /var/lib/agent-session-collector/receive
install -d -o asc_upload_alpha -g asc_upload_alpha -m 0700 \
  /var/lib/agent-session-collector/receive/sender-alpha/.ssh

# Allow only traversal of shared ancestors, not reads of peer data.
setfacl -m u:asc_upload_alpha:--x /var/lib/agent-session-collector \
  /var/lib/agent-session-collector/mirror \
  /var/lib/agent-session-collector/receive /etc/agent-session-collector

# Separate receiver config: same paths and receive.sender set as hub.toml,
# but no backend credentials; leave index emission disabled in this copy.
install -m 0600 -o root -g root examples/hub.example.toml \
  /etc/agent-session-collector/receive.toml
setfacl -m u:asc_upload_alpha:r-- /etc/agent-session-collector/receive.toml

# Hub jobs must create/delete inside nested receive-owned directories.
setfacl -R -m u:asc-hub:rwX,o::--- \
  /var/lib/agent-session-collector/mirror/sender-alpha
setfacl -R -d -m u:asc-hub:rwx,o::--- \
  /var/lib/agent-session-collector/mirror/sender-alpha
```

For additional senders, edit the existing `receive.toml` rather than reinstalling
the example, keep its paths/sender set synchronized with `hub.toml`, and add each
receiver's read ACL. Grant no receiver access to the hub group, catalog or archive.
Use a password-disabled account, dedicated keys only, and ensure sshd permits
public-key authentication for the account. The login shell executes the forced
command; it is not permission for unrestricted SSH.

Add the sender public key to the receive account with a forced command:

```text
command="/usr/local/bin/asc-receive -config /etc/agent-session-collector/receive.toml -sender sender-alpha",restrict ssh-ed25519 AAAA... sender-alpha
```

Keep private keys on sender hosts only.
Install `authorized_keys` with mode 0600, owned by its receive account. Verify
traversal/config access as that account before testing a real sender sync; verify
the hub user can create/delete a temporary file in a newly received subdirectory.
Check that one receiver cannot read a peer mirror or the archive/catalog.

## Sender Layout

On each sender:

```bash
install -d -m 0750 /etc/agent-session-collector
install -d -m 0750 /var/lib/agent-session-collector
install -m 0640 examples/sender.example.toml /etc/agent-session-collector/sender.toml
```

Generate or install an SSH key dedicated to the collector:

```bash
ssh-keygen -t ed25519 -f /var/lib/agent-session-collector/id_ed25519 -N ''
```

Configure the sender's SSH client so the `hub.ssh_target` in `sender.toml`
uses that key and the expected host key.

Preview:

```bash
asc-sender sync -config /etc/agent-session-collector/sender.toml -dry-run
```

Run:

```bash
asc-sender sync -config /etc/agent-session-collector/sender.toml
```

## Timers

Use systemd timers or another scheduler. A typical layout:

Sender service:

```ini
[Unit]
Description=agent_session_collector sender sync

[Service]
Type=oneshot
ExecStart=/usr/local/bin/asc-sender sync -config /etc/agent-session-collector/sender.toml
```

Sender timer:

```ini
[Unit]
Description=Run agent_session_collector sender sync

[Timer]
OnBootSec=2m
OnUnitActiveSec=10m
RandomizedDelaySec=60s
Persistent=true

[Install]
WantedBy=timers.target
```

Hub index service:

```ini
[Unit]
Description=agent_session_collector hub index

[Service]
Type=oneshot
User=asc-hub
Group=asc-hub
ExecStart=/usr/local/bin/asc-hub index -config /etc/agent-session-collector/hub.toml
```

Hub compaction service:

```ini
[Unit]
Description=agent_session_collector hub compact

[Service]
Type=oneshot
User=asc-hub
Group=asc-hub
ExecStart=/usr/local/bin/asc-hub compact -config /etc/agent-session-collector/hub.toml
```

Run index more frequently than compaction. For example: index every `10m`,
compact every `1h`.

## Verification

Sender:

```bash
asc-sender sync -config /etc/agent-session-collector/sender.toml -dry-run
systemctl list-timers 'agent-session*' 'agent_session*'
```

Hub:

```bash
sudo -u asc-hub asc-hub doctor -config /etc/agent-session-collector/hub.toml
sudo -u asc-hub asc-hub index -config /etc/agent-session-collector/hub.toml
sudo -u asc-hub sqlite3 /var/lib/agent-session-collector/catalog/sessions.sqlite 'select agent, count(*) from sessions group by agent;'
```

Archive integrity:

```bash
find /var/lib/agent-session-collector/archive -name '*.zst' -print0 |
  xargs -0 -r -n1 zstd -t
```
