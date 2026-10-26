# ADR-0177: An opt-in system log socket in the SSH sandbox

**Status:** Accepted (2026-10-26)
**Driven by:** a finding while building GH #2276: PHP Defense blocks in SSH, cron and Composer PHP never reach the incidents list. The maintainer asked for it as an option (2026-10-26).
**Related:** ADR-0067 (SSH shell sandbox), GH #1458 (cron in the sandbox), ADR-0176 (Composer runs), ADR-0088 (PHP Defense), GH #2262 (incidents trust the journal's `_UID`).

## Context

The sandbox (`jabali-ssh-shell`, bubblewrap) mounts a fresh `/dev` and a fresh
tmpfs on `/run`, so it has no `/dev/log`. PHP Defense logs its blocks with
`syslog()`, which writes to `/dev/log`. In the sandbox the call fails silently:
the block still happens, but no line reaches the journal, so
`snuffleupagus.fetch_incidents` never sees it. That covers SSH sessions, cron
jobs (GH #1458 runs them in the sandbox) and anything they start. On the test
box a blocked `system()` in cron PHP left no trace.

## Decision

1. **An admin opt-in, off by default.** `server_settings.ssh_sandbox_syslog`
   (migration 337), set under Server Settings → SSH → Shell Sandbox. It travels
   with the sandbox mode in `system.set_ssh_sandbox_mode` (key `syslog`), which
   writes `/etc/jabali/ssh-sandbox-syslog` (`on`, 0644) or removes it. An older
   panel that sends no `syslog` leaves the file alone.
2. **The wrapper binds only the socket.** When the flag file is a regular file
   holding `on`, `jabali-ssh-shell` adds
   `--ro-bind-try /run/systemd/journal/dev-log /dev/log` after `--dev /dev`.
   Nothing else under `/run/systemd` is visible. Any other content, a link, or a
   read error is off. The wrapper reads the flag on every connect, so no reload.
3. **Off stays the default on every server**, new and upgraded: the migration
   adds the column as 0, and no flag file means no socket.

## Consequences

- With it on, any program in the sandbox can write to the system log, under any
  tag. The journal readers that act on a line check the sender's uid, which
  journald takes from the kernel, and not the tag alone: the incident reader
  attributes a line to a site only when that uid is root or the site's owner
  (GH #2262), and CrowdSec's SSH source counts only lines with `_UID=0`
  (#2287). So a line from the sandbox can't put an incident on another
  account or feed CrowdSec's SSH scenarios. A tenant can still add noise to
  the journal and to their own incidents; journald's rate limits apply.
- Composer runs (ADR-0176) log PHP Defense blocks to the run's own log and are
  not affected.
- nspawn mode is not wired yet (it refuses logins); it would need the same bind
  when it is.
