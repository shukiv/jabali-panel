# ADR-0007: Agent v2 — unified background task control plane

**Date**: 2026-04-13
**Status**: accepted
**Deciders**: Shuki

## Context

Background work in Jabali is scattered across three incompatible execution models with no unified view:

| Subsystem | Current mechanism | Visibility |
|---|---|---|
| SSL issuance, git deploy, cPanel restore, WHM migration, IMAP sync | Laravel `ShouldQueue` jobs (Redis) | `filament-jobs-monitor` |
| jabali-backup | Bash CLI at `/opt/jabali-backup/bin/run`, systemd timer | None in panel — user must ssh |
| Addon install / uninstall | Agent then `systemd-run` shell | Ad-hoc JS poller on the addon tab |
| Certbot renewals | Cron then agent shell | Server logs only |

Three compounding problems:

1. **No unified dashboard.** An operator asked "what's running right now" has no single place to look. SSL renewal is in one page, backup progress requires SSH, addon installs surface only while the tab is open.

2. **Queue worker model is wrong for long jobs.** We just converted `jabali-queue` to an `--max-time=55 --stop-when-empty` oneshot timer to save ~190 MB RSS (see `install.sh` `setup_queue_service()`). This is the right tuning for short jobs but SIGKILLs backups 55 seconds in. A dedicated always-on `backups` queue reintroduces the RAM cost we just removed.

3. **Execution is coupled to progress reporting.** `queue job → agent → shell call → wait (hours)` blocks a PHP process for the entire run. Memory stays allocated. Cancellation is impossible without killing the worker.

Layered on top, two security concerns surfaced during design:

- **Socket equals root, no internal auth.** `/var/run/jabali/agent.sock` is `0660 root:www-data`. Filesystem permissions are the only gate. Any `www-data` process (panel RCE, compromised dep, WordPress RCE on the same box) can call any agent method. `www-data` is effectively root via the agent.
- **Shell invocation with `escapeshellarg()` is the floor, not the ceiling.** The shell is still parsing the string. Argument injection (`--flag` disguised as a value), locale issues, chained commands, and typos in command construction all bypass `escapeshellarg()`. The agent uses shell-invoking primitives throughout ~21k lines.

## Decision

Split background work into three planes, mirroring how control planes like Kubernetes separate API server, controller, and kubelet:

```
CONTROL PLANE          EXECUTION PLANE            STATE PLANE
─────────────          ────────────────           ───────────
Laravel panel    →     Agent + systemd      →     background_tasks DB
(schedules intent)     (runs reality)             (observed state)
```

Operating principle:

> **"The queue schedules intent. The agent executes reality. The DB is state. The UI observes."**

Concretely:

1. **`background_tasks` table** in the panel DB is the source of truth. Every long-running operation — backup, SSL, addon install, migration, deploy — creates a row before it starts, and every state change updates the row.

2. **Laravel queue workers become short-lived dispatchers.** A queue job's job is: insert the row, call `agent.job.start`, return. It never waits. The existing oneshot timer stays.

3. **Agent becomes a job dispatcher, not a blocking executor.** New socket methods (`job.start`, `job.status`, `job.cancel`, `job.list`, `job.tail`) accept a type and payload, spawn the work in a transient systemd unit via `systemd-run --unit=jabali-job-$id --scope --collect`, return immediately with structured `{id, unit, pid, started_at, log_path}`.

4. **Spawned binaries emit JSONL progress events** to a log file (`/var/lib/jabali/jobs/$id.log`). The agent tails the log, updates its own SQLite state store, and POSTs events to the panel via an internal localhost-only endpoint. Binaries never call the panel directly.

5. **Panel has one Filament "Background Tasks" resource** that surfaces everything — queue-dispatched, cron-dispatched, user-initiated — in a single view.

6. **Real-time UI via Laravel broadcasting** (Reverb). Polling is fallback, not primary. Progress bars update on event, not on interval.

7. **Reconciliation** via `jabali:tasks:reconcile` every 60s. Agent calls are best-effort; the reconciler is authoritative. If a POST is lost during a panel restart, the next reconcile cycle fixes state.

8. **Capability-based security** instead of "socket = blanket root." Every agent method registers a schema. Inputs are validated before dispatch. Unknown methods are rejected. There is no generic shell-escape method.

9. **Safe execution helper** (`runSafe(array $argv, ?string $stdin, int $timeout, array $env = [])`) using `proc_open` array form — no shell. Absolute paths only. Explicit empty env. Hot-path agent methods migrated first.

10. **Append-only audit log** at `/var/log/jabali/agent-audit.log` (0600 root). Every agent call: timestamp, method, args hash, caller uid (via `SO_PEERCRED`), result, duration.

11. **Panel uid separation** (final phase): panel runs as `jabali-panel`, not `www-data`. Socket becomes `0660 root:jabali-panel`. A compromised WordPress site on the same box can no longer open the socket.

## `background_tasks` schema

```
id              uuid (primary)
type            enum (backup, ssl_issue, ssl_renew, user_create,
                      addon_install, addon_uninstall, git_deploy,
                      cpanel_restore, whm_migration, imap_sync, ...)
target_type     string (polymorphic — domain, user, addon, ...)
target_id       string
status          enum (pending, running, done, failed, canceled)
progress        int nullable (0..100)
step            string nullable
started_at      timestamp nullable
finished_at     timestamp nullable
created_at      timestamp
updated_at      timestamp
systemd_unit    string (e.g. jabali-job-xyz)
pid             int nullable
exit_code       int nullable
error_message   text nullable (truncated 4KB)
retries         int default 0
max_retries     int default 0
payload         json
log_path        string
callback_token  string (secret, unique per task)
dedupe_key      string nullable
```

Indexes:
- `(status, type, created_at)` — dashboard queries
- `(target_type, target_id, status)` — policy checks
- unique `(dedupe_key)` where `dedupe_key IS NOT NULL AND status IN ('pending','running')` — prevents duplicate in-flight tasks

## Agent protocol

New socket methods:

| Method | Input | Output |
|---|---|---|
| `job.start` | `{type, payload, limits: {cpu, memory, io}, dedupe_key?}` | `{id, unit, pid, started_at, log_path}` or 409 dedupe |
| `job.status` | `id` | full row + last 10 log events |
| `job.cancel` | `{id, grace_sec?}` | final status |
| `job.list` | `{status?, type?, limit?}` | array of rows |
| `job.tail` | `{id, since_ts?}` | log events since timestamp |

Agent-local state store: `/var/lib/jabali/agent/jobs.db` (SQLite, WAL mode, mode 0600 root).

## Spawned binary contract

Any binary invoked via `job.start` must:

1. Accept `--task-id=<uuid>` and `--progress-log=<path>` arguments.
2. Emit one JSON object per line to `$progress_log`:
   ```json
   {"ts":"2026-04-13T01:30:00Z","event":"started"}
   {"ts":"2026-04-13T01:35:00Z","event":"progress","percent":42,"step":"uploading"}
   {"ts":"2026-04-13T01:45:00Z","event":"done","data":{"bytes":1048576000}}
   ```
   Valid events: `started`, `progress`, `step`, `done`, `failed`, `canceled`.
3. Exit `0` on success, non-zero on failure.
4. Handle `SIGTERM` gracefully (finish current batch, emit `canceled`, exit).
5. **MUST NOT call panel HTTP endpoints directly.** Only emit log events.

This is a stable contract. The jabali-backup team (and future addons) integrate against this, not panel internals.

## systemd-run invocation

```bash
systemd-run \
  --unit=jabali-job-${id} \
  --scope \
  --collect \
  --description="Jabali task ${type}#${id}" \
  --property=CPUQuota=${cpu_quota}% \
  --property=MemoryMax=${memory_max} \
  --property=IOWeight=${io_weight} \
  -- \
  ${binary} --task-id=${id} --progress-log=${log_path}
```

Benefits:
- Per-job cgroup limits — backups can't OOM the server
- `journalctl -u jabali-job-${id}` for free
- `systemctl stop jabali-job-${id}` is the cancel button
- Unit is collected on exit (no zombies)

## Alternatives Considered

### Alternative 1: Put everything through the Laravel queue with a dedicated long-running worker

- **Pros**: Single mechanism. Jobs are just `ShouldQueue`. `filament-jobs-monitor` already shows them.
- **Cons**: Always-on `queue:work --timeout=0 --queue=backups` brings back the ~190 MB RSS we just removed. Blocks a PHP process for the full duration of every run. No native cgroup limits. Cancellation requires killing the worker. Doesn't address non-queued work (jabali-backup CLI, certbot, agent shells).
- **Why not**: Regresses RAM, doesn't unify, poor fit for hours-long jobs.

### Alternative 2: Keep things separate — accept the scattered view

- **Pros**: Zero work.
- **Cons**: Doesn't solve the stated problem ("backup jobs, SSL job and all jobs in one place"). Operators continue to context-switch across three places.
- **Why not**: User explicitly asked for unified visibility.

### Alternative 3: Panel writes to `background_tasks`; each binary POSTs progress back to panel directly

- **Pros**: No agent-side state store.
- **Cons**: Every binary needs an HTTP client + auth tokens + retry logic. Tight coupling between addons and panel internals. jabali-backup team has to know about panel API contracts. Breaks the privsep boundary — root binaries shouldn't need to know panel auth schemes.
- **Why not**: Wrong coupling. Agent is the natural privileged integration point; addons should only need to know the log format.

### Alternative 4: Adopt an existing job queue system (Horizon, Temporal, etc.)

- **Pros**: Battle-tested, rich feature sets.
- **Cons**: None of them solve the "spawn as root with cgroup limits" problem. Still need the agent for privilege escalation. Still need a translation layer to unify non-queue work.
- **Why not**: Orthogonal to the core problem, which is "how do we run and observe root-privileged long jobs."

### Alternative 5: Use eddie-rusinskas/filament-queueable-bulk-actions as "the job manager"

- **Pros**: Already exists.
- **Cons**: Solves a different problem — it runs Filament table bulk actions through the queue with progress. It is not a dashboard of all background work.
- **Why not**: Misnamed solution to the stated problem.

## Consequences

### Positive

- **Single dashboard for all background work.** Operators look at `/jabali-admin/background-tasks` and see everything.
- **No RAM regression.** Short-lived queue dispatchers stay on the existing oneshot timer.
- **Per-job resource limits.** A runaway backup can't starve the panel; cgroup limits are kernel-enforced.
- **Cancellation works.** `systemctl stop jabali-job-${id}` is reliable; SIGTERM then SIGKILL with grace period.
- **Crash-safe.** State is on disk; reconciler catches drift; agent restart doesn't lose jobs.
- **Decoupled addons.** jabali-backup and future addons integrate via a 5-line JSONL contract — no panel internals.
- **Audit trail.** Every privileged call is logged. Incident response has a timeline.
- **Safer exec surface.** New code uses `runSafe()` (no shell, no PATH, no interpretation).
- **WordPress compromise no longer implies panel compromise** (after Phase 12).
- **The architecture scales.** Adding a new background work type is one schema row, one binary, one Filament action — not a new dashboard, not a new lifecycle, not a new monitoring story.

### Negative

- **Complexity is higher.** Three-plane architecture has more moving parts than "queue or cron."
- **Agent SQLite is a new failure mode.** WAL corruption, disk full, permission drift — another thing to operate.
- **The contract pins the jabali-backup team.** They have to implement `--task-id` / `--progress-log` / JSONL output. External coordination cost.
- **Broadcasting infrastructure (Reverb).** Another daemon to operate. Optional until Phase 11.
- **Migration is long.** ~110 engineer-hours across 13 phases. Not a one-weekend project.

### Risks

- **SQLite concurrency in the agent.** Single-threaded PHP agent handling multiple simultaneous `job.start` calls. Mitigation: WAL mode, short transactions, per-row locks, test with 50 concurrent dispatches in CI.
- **Event delivery loss.** Agent POST to panel fails during restart. Mitigation: 60s reconciler is authoritative; events are advisory.
- **First migration (SSL) regression.** Breaking cert issuance blocks every new domain. Mitigation: feature flag `jabali.agent_v2.ssl_enabled`, keep legacy `IssueSslCertificate` intact, opt-in rollout by domain or by env.
- **jabali-backup team coordination.** External dependency. Mitigation: publish contract doc + mock binary early so they can develop in parallel.
- **uid separation (Phase 12) breaks file ownership.** `storage/`, `bootstrap/cache/`, uploaded files, log files all owned by `www-data` today. Mitigation: dedicated release; automated migration script; documented rollback; stage on test server first.
- **systemd-run cgroup delegation on older hosts.** Requires cgroups v2 (Ubuntu 22.04+, Debian 12+), which we already require (see ADR-0006). No additional risk.

## Implementation phases

See `plans/agent-v2-control-plane.md` for the full 13-phase plan. Summary with dependencies:

```
0 ADR ──┬──► 1 Schema ──┐
        └──► 2 Safe-exec/audit ──┤
                                 ▼
                          3 Agent job.* ──┬──► 4 Event endpoint ──┐
                                          ├──► 5 Filament resource ┤
                                          └──► 6 Reconciler ───────┤
                                                                   ▼
                                                          7 SSL migration ──┬──► 8 Backup integration
                                                                            ├──► 9 Remaining jobs
                                                                            ├──► 10 Capability system
                                                                            ├──► 11 Broadcasting/ops
                                                                            └──► 12 uid separation
```

Each phase is independently shippable. Legacy code paths stay intact until every caller is migrated.

## Migration strategy

- **No big-bang cutover.** Phases 7+ migrate one subsystem at a time, feature-flagged.
- **Legacy methods are deprecated, not deleted,** until every caller moves. A grep check in CI blocks *new* shell-invoking call sites once migration begins; existing usage is tolerated during transition.
- **Rollback per phase** is documented in `plans/agent-v2-control-plane.md`. For each phase: the feature flag to flip off, the code paths that remain, the DB rows to clean up if any.
- **Test server first.** 10.0.3.13 LXC is the proving ground. Production rollout only after test server runs Phase 7+ for a week without regression.

## Out of scope for this ADR

- Per-tenant resource quotas beyond cgroup limits (users' jobs share the panel's own quota pool — individual tenant isolation is future work).
- Multi-host orchestration. Single-node only.
- Scheduled / cron-like task definitions — Laravel Scheduler remains the source of recurring schedules; it dispatches via the new runner, but scheduling UX is unchanged.
- Historical task analytics / metrics beyond basic counts.
- The NotificationSystem v2 work from Issue #102.

## References

- Plan: [`plans/agent-v2-control-plane.md`](../../plans/agent-v2-control-plane.md)
- Previous ADRs:
  - [ADR-0004: Standalone backup tool](0004-standalone-backup-tool.md) — explains why jabali-backup lives in a separate repo
  - [ADR-0005: Agent addon routes](0005-agent-addon-routes.md) — the addon then agent.d pattern this ADR extends
  - [ADR-0006: cgroup v2 resource limiting](0006-cgroup-v2-resource-limiting.md) — prerequisite for per-job limits
- Laravel queue docs: https://laravel.com/docs/12.x/queues
- `systemd-run(1)`: https://www.freedesktop.org/software/systemd/man/systemd-run.html
- `SO_PEERCRED` for socket auth: https://man7.org/linux/man-pages/man7/unix.7.html
