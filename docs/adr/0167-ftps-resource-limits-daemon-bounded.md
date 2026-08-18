# ADR-0167 — FTPS resource limits are vsftpd-daemon-bounded, not per-tenant cgroup

- Status: Accepted
- Date: 2026-08-18
- Tracks: JAB-263 (FTPS sessions bypass per-tenant cgroup resource limits)
- Related: JAB-259 / JAB-260 (FTP-as-reconciled-resource epic), ADR-0028 (M12 SFTP)

## Context

The M18 per-tenant resource limits (`user.limits.apply`) write cgroups-v2
directives — `CPUQuota`, `MemoryMax`, `TasksMax`, IO weight — into
`jabali-user-<tenant>.slice.d/limits.conf`. The kernel enforces those on the
cgroup `jabali.slice/jabali-user.slice/jabali-user-<tenant>.slice`.

A process is only subject to that slice's limits if it actually lives in the
slice's cgroup. In this codebase only **panel-managed services** are placed
there — the per-user php-fpm pool, python-app units, docker apps, and CLI
work run via `systemd-run --slice=jabali-user-<tenant>.slice` — each through an
explicit `Slice=` in its unit or a `systemd-run` flag.

**Interactive sessions are not.** vsftpd forks a worker per login under
`vsftpd.service`'s own cgroup; the worker never migrates to the tenant slice.
The vsftpd PAM stack (`vsftpd-jabali`) has no `pam_systemd`, so logind opens no
session for it — and even with `pam_systemd`, logind's default target is the
standard `user-<uid>.slice`, **not** the panel's `jabali-user-<tenant>.slice`.
The identical gap applies to SSH/SFTP interactive logins: they too fall outside
the tenant slice. (A prior code comment in `ftp_account.go` wrongly claimed
FTPS sessions "land in the tenant's slice, so M18 limits apply unchanged" — they
do not. That comment is corrected.)

Placing an already-forked worker into the tenant slice is not a small change:

- cgroups-v2's **no-internal-process rule** forbids bare processes in a cgroup
  that has controller-enabled children — and `jabali-user-<tenant>.slice`
  already has service children — so a per-session **leaf scope** under the slice
  is required, not a direct `cgroup.procs` write.
- vsftpd cannot be launched through `systemd-run`, and `systemd-run` cannot
  move an **existing** PID into a scope, so placement would need a `pam_exec`
  session hook creating a transient scope without racing logind/systemd.
- A hook that fails closed would break FTPS logins outright — a worse outcome
  than the accounting gap it closes.

## Decision

Bound FTPS resource consumption at the **vsftpd daemon** and document the
boundary honestly, rather than build a fragile per-session cgroup-placement
hook for FTPS alone.

The daemon-level caps (rendered into `/etc/vsftpd.conf` from `server_settings`,
pinned by `install/tests/test_ftp_module_optin.sh`):

- `max_clients` — total concurrent FTPS connections host-wide. **Always on**,
  default 50.
- `max_per_ip` — concurrent connections from one source IP. **Always on**,
  default 8.
- `local_max_rate` — per-session transfer-rate ceiling (bytes/s). **Opt-in**:
  default 0 = unlimited; set only when the operator populates
  `ftp_local_max_rate_kbs`.

Correct the false `ftp_account.go` comment; state the boundary in the FTP
runbook.

**Deferred:** true per-tenant cgroup placement of interactive sessions is a
cross-cutting concern (SSH + FTPS share the gap) and is tracked with the
FTP-as-reconciled-resource epic (JAB-259 / JAB-260). It is deliberately not
solved per-ticket, per-protocol.

## Consequences

- **Honest posture.** Nothing claims a per-tenant cgroup boundary that does not
  exist. Operators reading the runbook know FTPS is daemon-bounded.
- **Residual gap.** There is no per-*tenant* connection cap: a tenant behind one
  IP is bounded by `max_per_ip` (8), but a tenant spread across many IPs is
  bounded only by the global `max_clients` (50) and could consume a large share
  of slots. With `local_max_rate` at its default (0 = unlimited), each of those
  sessions can transfer at full line rate, so an operator who wants a bandwidth
  floor for other tenants must set `ftp_local_max_rate_kbs`. CPU/memory of FTPS
  workers is not attributed to the tenant slice, so usage reporting under-counts
  interactive transfers.
- **Re-scope acknowledged.** JAB-263's literal acceptance ("FTPS PIDs under
  `jabali-user-<tenant>.slice`") is not met; it moves to the epic. This ADR
  records the operator-approved re-scope.
- **Reversible.** If the epic builds session placement, this daemon boundary
  stays as defense-in-depth; nothing here blocks it.
