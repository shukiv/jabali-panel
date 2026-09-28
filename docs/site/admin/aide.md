# AIDE — Host Integrity

Security → AIDE. Daily comparison of the host's filesystem against a baseline AIDE database.

## What AIDE watches

`/etc/aide/aide.conf.d/jabali.conf` (managed by the panel) scopes AIDE to:

- `/bin`, `/sbin`, `/usr/bin`, `/usr/sbin`, `/usr/local/bin`, `/usr/local/sbin`
- `/etc` minus expected mutable paths (`/etc/letsencrypt/`, `/etc/jabali/db-password`, etc.)
- `/lib`, `/lib64`, `/usr/lib`
- `/boot`

Things deliberately excluded: `/var`, `/tmp`, `/home`, `/proc`, `/sys`, mailbox stores, database data dirs. These change constantly by design; checking them would drown the signal.

## Schedule

`aide.timer` runs daily, by default at 03:30 local. Configurable under Server Settings → Security → AIDE schedule.

## What the page shows

- Time of last scan and result (clean / drifted).
- Number of added / changed / removed files in the most recent scan.
- Per-file diff list for the last scan (path, what changed — content, permissions, owner, size).
- Trend chart of diff counts over the last 30 days.

## Per-row actions

- **Trust** — accept the change as legitimate; AIDE updates its baseline so the file is no longer reported as drifted.
- **Investigate** — opens the kernel-level file metadata (modify time, owner, SELinux context if available) plus, if the file is small and text, a hex preview.
- **Restore** — write the baseline-known content back. Only available when AIDE captured the original content (off by default for size reasons).

## Notifications

The `aide.tamper.detected` event fires when a scan reports a change to a monitored file. It goes to the bell and to every enabled channel; see [Notifications Routing](./notifications-routing.md).

## Trust before incident

The first few `jabali update` runs after install will produce AIDE diffs as the panel writes new files. Trust these once; the baseline is updated and subsequent runs report only genuine drift.

## When AIDE fires alone

If AIDE fires `aide.tamper.detected` and nothing else has changed (no `jabali update`, no operator action), treat it as a high-severity signal: a file outside the panel's drop-in paths has been modified. Pair with [Audit Log](./audit-log.md) and [CrowdSec Decisions](./crowdsec-decisions.md) to triangulate.

## CLI

```bash
sudo aide --check                 # ad-hoc scan
sudo aide --update                # rebuild baseline
sudo systemctl start aide.service # immediate run via systemd
```
