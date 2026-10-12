# ADR-0067: SSH shell sandbox (bubblewrap default; nspawn opt-in)

**Status:** Accepted
**Date:** 2026-04-26
**Deciders:** Shuki (operator/architect)
**Supersedes:** —
**Related:** M12 SFTP, 7480fff (SFTP chroot)

## Context

Hosting users with SSH access (package.ssh_enabled = true) currently get
a plain bash shell on the host. They can read every world-readable file
on the system, see other tenants' processes, and discover internal panel
infrastructure. UID-based isolation is the only barrier.

We need a sandbox for SSH-shell sessions that:
- Hides the host filesystem outside `/home/<user>` and the bare
  read-only system needed to run a shell
- Prevents other tenants from being enumerated (process list, /home,
  /etc/passwd)
- Doesn't break wp-cli/composer/git network egress
- Stays maintainable: one wrapper file, no per-user sshd config drop-ins

## Decision

**Two sandbox modes, no plain-bash mode.**

1. `bubblewrap` (default) — namespace + bind-mount sandbox via setuid
   `bwrap`. Lightweight. Host kernel + filtered host filesystem.
2. `nspawn` — `systemd-nspawn --ephemeral` against a versioned,
   immutable base image at `/var/lib/jabali-nspawn/images/<codename>-<version>/`.
   Heavier (~250 MB rootfs). Full filesystem isolation.

**Wrapper-as-login-shell, not ForceCommand.**

`/usr/local/bin/jabali-ssh-shell` is set as every hosting user's login
shell at user-create. Wrapper reads `/etc/jabali/ssh-sandbox-mode` on
each connect, exec's the matching backend. SFTP users hit
`ForceCommand internal-sftp` from the existing Match block — wrapper
never runs in that path. Defense-in-depth: if Match block ever
misconfigures, SFTP user lands in a sandboxed shell, not bare bash.

**Failure mode = `/usr/sbin/nologin`. Never bash.**

Wrapper exits to nologin when the mode file is missing, the value is
unrecognized, or (for nspawn) the pinned image directory is missing.
No path through the wrapper ever produces an unsandboxed shell.

**Per-user pinned nspawn image version.**

`users.nspawn_image_version` column. New SSH-enabled users stamped with
`default_nspawn_image_version` at user-create. Existing users keep their
pin even after the default bumps. Image upgrade is always an explicit
admin action (per-user or bulk).

**Deterministic builds via apt snapshot pinning.**

`jabali nspawn-build --snapshot 20260426T000000Z` is mandatory; rejects
without `--snapshot`. debootstrap targets
`https://snapshot.debian.org/archive/debian/<TS>/`. MANIFEST.json
captures the snapshot timestamp, package list, and rootfs SHA-256.

**No host sockets bound into either sandbox.**

`/run/php/jabali-<u>/`, `/run/php/php-fpm.sock`, `/run/mysqld/mysqld.sock`,
`/run/jabali-agent.sock`, every Kratos/panel/admin socket, `/var/lib/jabali-*`,
`/etc/jabali/`, `/run`, `/var/run` are all hidden. Sandbox reaches host
services only via TCP/HTTP on host interfaces. Implication: in-sandbox
`wp-cli`/`mysql` CLI cannot reach MariaDB or PHP-FPM. Documented
escapes: panel UI, phpMyAdmin SSO, Applications "Run command". A scoped
TCP proxy is deferred to a future ADR.

**Amendments.** Two sockets are now bound into the bubblewrap jail, each
gated by its own authentication, so the jail adds no access the tenant's
PHP-FPM pool (same uid) doesn't already have:

- `/run/mysqld/mysqld.sock` (GH #285): MariaDB auth and grants gate it.
- `/run/redis/redis.sock` (GH #2059): the Redis `default` user is off
  (ADR-0148). A connection needs the tenant's own ACL credential
  (`t_<user>`, GH #1016), which reaches only `jt:<user>:*`. The socket's
  group ACL (`jabali-redis-clients`) still applies.

Only the socket files are bound, not their directories. Every other host
socket stays hidden.

## Alternatives considered

**`Match User <u>` with `ForceCommand /usr/local/bin/jabali-ssh-shell`.**
Rejected. Adds one sshd_config drop-in per user → mass sshd reloads on
every user change. Wrapper-as-shell is one chsh per user (idempotent),
no sshd touch.

**Per-user sandbox mode.**
Rejected for v1. Server-wide flag covers the operator use case ("am I
running tight or loose?"). Per-user mode adds a column, an admin UI for
it, and a reconciler branch with negligible payoff. Per-user IMAGE
version (within nspawn mode) IS supported because that maps to upgrade
risk per tenant.

**Shared mutable nspawn rootfs.**
Rejected. One operator-driven `apt upgrade` inside the rootfs would
silently change every user's environment between connects. Versioned
immutable images make every change visible and pinnable.

**Bind `/run/php/jabali-<u>/fpm.sock` into the sandbox.**
Rejected. FPM socket is a privilege channel: a compromised sandbox can
replay any FPM request the nginx vhost would. The principle of "no host
sockets bound" trumps the wp-cli convenience.

**Bind `/run/mysqld/mysqld.sock` (relying on MariaDB ACLs).**
Rejected for v1. MariaDB skip-networking is on per M25; binding the
socket re-introduces a direct DB path from the user-controlled
environment. ACLs are enforcement; the principle is "don't expose
control planes inside the sandbox at all."

**Plain-bash "none" mode for backward compat.**
Rejected. The user-visible escape is "make this user SFTP-only" — the
existing access mode for "no shell". Keeping a "none" mode would
preserve the current insecure-by-default posture indefinitely.

**Default to `nspawn` instead of `bubblewrap`.**
Rejected. nspawn requires a 250 MB rootfs build at install time, adds
1-3s connect latency, and depends on snapshot.debian.org reachability.
Bubblewrap is purely local + zero-cost. Operators who want stronger
isolation flip the mode flag.

## Consequences

**Positive**
- Every SSH shell is sandboxed. No "did the operator remember to enable
  isolation?" question.
- Mode toggle is a one-file write. No sshd reload, no agent fan-out.
- Pinned per-user images give tenant-level upgrade control.
- Deterministic image builds let us reproduce exact production rootfs
  for debugging.

**Negative**
- In-sandbox `wp-cli`/`mysql` CLI cannot reach MariaDB or FPM. Users
  must use panel UI / phpMyAdmin / Applications "Run command" for those
  workflows until v2 ships a scoped TCP proxy.
- nspawn rootfs is ~250 MB on disk. Image versions accumulate; operator
  must run `jabali nspawn-prune` periodically.
- `chsh` on every existing hosting user at first reconciler sweep
  post-deploy — one-time mass change.
- snapshot.debian.org is an external dependency for image rebuilds.
  Documented mitigations: blessed snapshot bundled at install; reverse
  proxy fallback (`deb.debian.org/debian-snapshot`) in runbook.

**Neutral**
- v1 leaves egress network open inside the sandbox (composer/git/
  wp-cli need it). `--unshare-net` + bridge is a separate design.

## Implementation pointer

See `plans/m13-ssh-shell-sandbox.md` for the 12-step / 5-wave delivery
plan and `plans/m13-ssh-shell-sandbox-runbook.md` for operator workflows
(image build, upgrade pipeline, prune, mode-switch ops).
