# AIDE — Host Integrity

Security → AIDE. Daily comparison of the host's filesystem against a baseline AIDE database.

## What AIDE watches

The panel writes its own `/etc/aide/aide.conf` (the stock `/etc/aide/aide.conf.d/` fragments are removed). It watches:

- `/bin`, `/sbin`, `/usr/bin`, `/usr/sbin`, `/usr/local/bin`, `/usr/local/sbin`
- `/lib`, `/lib64`, `/usr/lib`
- `/etc`, minus the paths the panel writes to (`/etc/jabali`, the Let's Encrypt directories, the panel's nginx sites, PHP-FPM pools, systemd units and cron files) and files that change on their own (`/etc/passwd-`, `/etc/resolv.conf`, `/etc/machine-id`, the SSH host keys, and so on)
- `/boot`, `/root`

Not watched: `/var`, `/run`, `/tmp`, `/home`, `/proc`, `/sys`, `/dev`, `/mnt`, `/media`. These change all the time by design; checking them would drown the signal.

## Schedule

`jabali-aide-check.timer` runs the check daily at 04:30 UTC, plus a random delay of up to 15 minutes.

## What the page shows

- The age of the baseline (DB age) and the time of the last check.
- The number of added, changed and removed files in the last check, grouped by likely source (for example system package, panel-managed, security control, unknown).
- A list of up to 50 changed files, with a filter by source and by change type, and the old and new values when the report has them. **Download full diff** saves the report, from the top, up to 8 MB.

## Actions

- **Run check now** starts a check in the background, the same one the daily timer runs. A check takes a few minutes; the page shows it while it runs and updates when it ends.
- **Rebuild (dry run)** shows what a rebuild runs, and changes nothing.
- **Rebuild baseline** replaces the baseline with the current state of the files. It runs `aide --init` as root in the background and takes a few minutes; the page shows it while it runs. When it ends, the page says the baseline was rebuilt and no check has run since. If it fails, the page shows AIDE's error. The action is recorded in the [Audit Log](./audit-log.md).

A rebuild accepts every file as it is now. Review the changes first: if the server is compromised, a rebuild makes the changed files part of the baseline.

## After a deliberate change

Package and kernel updates, and edits under `/etc`, show up as changes. Review them, then rebuild the baseline from this page or on the server:

```bash
jabali aide rebuild --full                              # the whole baseline
jabali aide rebuild --paths '^/usr/local/bin/jabali-'   # only matching paths; refuses if other paths changed
```

## Notifications

The `aide.tamper.detected` event fires when a scan reports a change to a monitored file. It goes to the bell and to every enabled channel; see [Notifications Routing](./notifications-routing.md).

## When AIDE fires alone

If AIDE fires `aide.tamper.detected` and nothing else has changed (no `jabali update`, no operator action), treat it as a high-severity signal: a file outside the panel's drop-in paths has been modified. Pair with [Audit Log](./audit-log.md) and [CrowdSec Decisions](./crowdsec-decisions.md) to triangulate.

## CLI

```bash
jabali aide status          # baseline age and the last check
jabali aide check           # check now
jabali aide rebuild --full  # rebuild the baseline
```
