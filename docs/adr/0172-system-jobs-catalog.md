# ADR-0172: System jobs come from a fixed catalog; no disable from the list

**Status:** Accepted (2026-10-01)
**Driven by:** GH #1686 (reporter request for a System / Panel jobs section under Admin → Cron Jobs; shape confirmed in the issue).
**Related:** ADR-0065 (server status and the service allow-list), ADR-0118 (Updates Center auto-update timers).

## Context

Jabali installs about twenty systemd timers on a server: malware signature
updates and scans, AIDE, CrowdSec hub refreshes, AppArmor and outbound-traffic
enforcement, retention sweeps, disk maintenance, the GoAccess reports, and the
two update timers the Updates page manages. Admins had no view of them in the
panel: whether they ran, whether the last run worked, when they run next. They
had to use `systemctl list-timers` and `journalctl` from a shell.

Giving the panel this view means letting it read unit state and journals as
root and start units. A panel request must not be able to point those verbs at
an arbitrary unit, such as `jabali-panel.service` or a tenant's
`jabali-cron-*` timer.

## Decision

1. **A fixed catalog, compiled into both binaries.** `internal/systemjobs` lists
   every job: id, timer unit, service unit, label, description, category, and
   whether Run now is allowed. A request names a catalog id. The agent looks
   up the unit names itself, so no caller can name a unit. panel-api checks
   the same catalog first and answers 404 or 422 before calling the agent.
   The agent RPC payload types live in the same package, so the panel and the
   agent cannot drift.
2. **Run now only where it is meaningful.** It is refused for the two update
   timers, because the Updates page runs them and shows their progress, and for
   the single-sign-on cleanup, which already runs every 30 seconds. The agent
   enforces this as well as panel-api. A run uses `systemctl start --no-block`
   and is refused when the job is already running.
3. **Nothing can be disabled from the list.** The security jobs must stay on.
   The jobs with a user-facing on/off switch (the update timers, backup
   schedules) already have one on their own pages, and the list links there
   instead of offering a second editor.
4. **The state shown is systemd's.** A run's success is the service's `Result`,
   not its exit code, because some units declare extra success codes (AIDE
   exits non-zero when it reports changes). Next run times come from
   `systemctl list-timers --output=json`, whose times are epoch microseconds.
   They are not parsed from `systemctl show` text, which prints local-time
   strings with zone abbreviations that Go cannot parse reliably. If
   list-timers fails, the next run is left empty rather than guessed.
5. **Server-wide backup schedules appear as link rows.** The schedules with no
   owning tenant are listed with their schedule and next run, and they link to
   Backups. Tenants' own schedules are tenant jobs and stay out of the list.

## Consequences

**Positive**

- Admins see every scheduled job, its health, and its log in one place, and
  can trigger maintenance without a shell.
- The root-level verbs cannot reach any unit outside the catalog. A
  compromised panel cannot start a self-update or an OS upgrade through them.
- A job a box does not have (for example the free-hostname heartbeat) is
  simply not shown.

**Negative**

- A new timer in `install.sh` does not appear until it is added to the catalog.
  The catalog lives next to the other allow-lists, which already work this way.
- The label and description of each job are kept by hand.

## Alternatives considered

- **Discover timers by pattern (`jabali-*.timer`).** This shows new timers
  without a catalog entry. It was rejected because the pattern also matches
  tenant and root cron timers (`jabali-cron-*`) and stale units that are still
  loaded after their file was removed. A pattern also turns Run now into "start
  whatever matches", which is the opposite of an allow-list.
- **Enable/disable for jobs where off is safe.** Only the GoAccess reports
  qualified. It was left out to keep one rule: nothing is switched off from
  this list.
