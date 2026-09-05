# SFTP, SSH Shell & Keys

M12 (SFTP) + M13 (sandboxed SSH shell).

## What's exposed

- SFTP over the system `sshd` on port **22**, restricted to panel users via `Match Group jabali-sftp` in `/etc/ssh/sshd_config.d/jabali-sftp.conf`.
- **SFTP-only users** (the default) get `ForceCommand internal-sftp`,
  `X11Forwarding no`, `AllowTcpForwarding no`, chrooted to `/home/<user>`
  (read-write inside, no escape). No interactive shell.
- **SSH-shell users** (those on a hosting package with SSH enabled) get an
  interactive shell, but it always runs **inside a sandbox** — never a
  bare shell on the host. See [SSH shell sandbox](#ssh-shell-sandbox-m13).

## SSH key management

Each user manages their own keys at `/jabali-panel/ssh-keys`:

- Paste a public key (`ssh-ed25519 …` or `ssh-rsa …`); the panel validates the format.
- Name the key (e.g. "Laptop", "CI").
- Revoke any key from the same page.

The agent writes `~/.ssh/authorized_keys` with `0600` permissions owned by the user. A reconciler tick re-syncs the file on demand and on schedule (15 min, hash-cached so a no-op tick is free).

## SSH shell sandbox (M13)

Every panel user's login shell is `/usr/local/bin/jabali-ssh-shell`, a
small wrapper. SFTP-only users never reach it (`ForceCommand internal-sftp`
wins in their `Match` block). Users on an **SSH-enabled** package get an
interactive shell through the wrapper, which drops them into a sandbox.

There is **no "plain shell" mode** — sandboxing is mandatory for shell
users. An admin who wants no shell at all makes the user SFTP-only.

### Modes

Server-wide setting (Server Settings → SSH Access), one of:

- **`bubblewrap`** (default) — a namespace + bind-mount jail via the
  setuid `bwrap` binary. Lightweight, no image to build. **This is the
  shipped, working path.**
- **`nspawn`** — an ephemeral `systemd-nspawn` container off a versioned
  base image. *Not yet wired for login in this release — the mode can be
  selected and persisted, but the per-user nspawn image path is still unused
  (`install.sh`: "currently unused; Step 3 follow-up"). Use bubblewrap, the
  shipped, supported path.*

The mode is a single file, `/etc/jabali/ssh-sandbox-mode`, read fresh on
every connect — no sshd reload needed to switch.

### What the bubblewrap sandbox gives the user

- Their own `/home/<user>`, read-write.
- A read-only system (`/usr`, libraries, a shell) — enough to run
  `git`, `composer`, `wp-cli`, editors, etc.
- **Network egress stays open** so those tools work.
- `scp`, `rsync`, and `git-over-ssh` work (the wrapper forwards the SSH
  command into the sandbox).

### What it hides

- `/etc/shadow`, `/etc/jabali`, `/etc/ssh`, and every other tenant's
  `/home/*` — invisible.
- A **filtered `/etc/passwd` / `/etc/group`**: only system accounts plus
  the connecting user, so other tenants can't be enumerated.
- Other tenants' **processes** (private PID namespace — `ps` shows only
  the session's own processes).
- Every host **Unix socket** (PHP-FPM, MariaDB, the agent, Kratos, …):
  `/run` is a fresh tmpfs. In-sandbox `mysql` / `wp-cli db` therefore
  cannot reach MariaDB over its socket — use the panel's database tools
  or phpMyAdmin SSO for DB work.

### Failure mode

If anything is misconfigured (mode file unreadable, `bwrap` missing or
not setuid, an unrecognised mode), the wrapper exits to
`/usr/sbin/nologin` — it **never** falls through to an unsandboxed shell.
The user can still SFTP.

## FTP / SFTP subaccounts (GH #1053, #1145)

Beyond the main account login, a tenant can create **FTP/SFTP subaccounts** —
extra login users scoped to a subdirectory of the account. The whole surface is
**admin opt-in** (an admin enables it and sets a per-package cap) before tenants
see it:

- **Tenant self-service** page to add / reset / delete subaccounts, with a
  **password generator** on the create + reset forms (GH #1053).
- **Isolation modes**: a lighter **same-uid alias** (shares the account's uid),
  or **true separate-uid isolation** — a dedicated system user in its own jail
  with its own quota and ACLs (GH #1145). Separate-uid is the default where a
  filesystem quota exists; where quota is absent it falls back to off.
- **vsftpd module**: plain FTP(S) is an **opt-in, PAM-gated** module that stays
  masked while off (GH #1053). SFTP subaccounts work through the existing `sshd`.
- **Operator-tunable server limits**: max sessions, per-IP limit, transfer rate
  (GH #1053).
- **WebDAV** access on a subaccount (GH #1146) — a per-subaccount `jabali-webdav`
  worker, so the same scoped directory is reachable over WebDAV as well as
  FTP/SFTP.

The panel surfaces **observed-vs-desired** FTP state so drift between the DB rows
and the live system is visible rather than silent.

## SSH TCP forwarding (opt-in)

SFTP/shell users get `AllowTcpForwarding no` by default. An admin can grant a
**durable per-user opt-in for SSH TCP forwarding** (GH #1229) — e.g. for VS Code
Remote-SSH — which is firewall-guarded and applied via the sshd `Match` block.
It is off unless explicitly enabled.

## Password auth

Disabled by default for panel users. SSH keys are required. Admin can enable password auth per-user via Users → Edit → SSH section, but it is not the default.

## Connection example

```bash
sftp -i ~/.ssh/jabali-key alice@example.com
```

Or with `lftp`, FileZilla, Cyberduck — anything that speaks SFTP.

## What about FTP?

Not shipped. FTP is plaintext; recommending it would undermine the panel's TLS-everywhere stance. Use SFTP.

## What about root SSH?

The host's root account is unchanged; this matters only for the operator, not for panel users. The Jabali installer does not enforce a password-vs-key policy for root — that's your call.
