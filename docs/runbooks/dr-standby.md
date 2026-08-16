# Disaster-recovery standby — operator runbook

Covers the GH #331 DR standby: a **one-way async warm standby with manual
promotion**. There is no automatic failover, no quorum, and no active/active —
the operator pointing traffic at the promoted box is the fencing step. Read the
blueprint at `plans/gh331-dr-standby-node.md` for the design rationale.

## What it is (and is not)

A standby is a **read-only replica** of a primary's control plane. It pulls the
primary's system backups from a shared read-only backup destination and restores
the panel database, panel config, and TLS material into a **non-serving** posture.
It serves no live traffic and refuses tenant-provisioning writes until an operator
runs `jabali dr promote`.

It is NOT a load balancer, NOT a hot failover, and NOT a substitute for backups.
The standby never holds a primary-mutating credential — the only channel between
the two boxes is the one-way backup destination (least privilege).

## Quick reference

| What | Where |
|---|---|
| Role flag | `server_settings.server_role` = `primary` (default) \| `standby` |
| Pairing | `server_settings.dr_destination_id` / `dr_peer_label` / `dr_paired_at` |
| Sync liveness | `server_settings.dr_last_sync_at` / `dr_last_snapshot_id` / `dr_last_sync_status` / `dr_last_sync_error` |
| CLI | `jabali dr status \| pair \| unpair \| feed \| promote` |
| Write refusal | `middleware.StandbyReadOnly` — a standby 409s mutating API calls (allowlist: `/admin/dr`, `/admin/settings`) |
| Pull loop | `panel-api/internal/drsync` — restores newest `system_backup` each tick; inert unless `server_role=standby` |
| Reconciler gate | a standby's `ReconcileAll`/`ReconcileOne`/SSL loops early-return (no serving config, no ACME) |
| Banner | `/me/server-capabilities` → `is_standby`; `DRStandbyBanner` renders on every page |
| Migrations | `000254` (role + pairing), `000255` (sync liveness) |

## Prerequisites

1. A shared backup destination both boxes can reach, created on the PRIMARY with
   `jabali backup destination create` (S3/B2/SFTP/etc). This is the DR channel.
   Use an address the STANDBY can also reach — never `127.0.0.1`/`localhost`:
   after the first sync the standby runs on the primary's replicated destination
   row (see below), so a localhost address would poison every later pull.
2. The same destination created on the STANDBY with the **same name** and the
   same connection details. Each applied sync replaces the standby's
   `backup_destinations` rows with the primary's, and the standby re-finds its
   DR channel **by name** — a name mismatch strands the pairing on a dangling
   destination id (`dr status` shows "destination not found").
3. Copy the primary's `/etc/jabali-panel/restic-repo.password` onto the standby
   (same path, root:root 0600) before pairing, unless the destination carries
   its own per-destination password. The repo is sealed with the primary's
   password, and that file is deliberately excluded from replication.
4. The standby box installed with the same Jabali version as the primary. A
   standby running an older binary against a newer schema will fail to apply the
   restore — keep versions in lockstep.

## Setup

### On the primary — start shipping to the DR channel

```
jabali dr feed --destination <dest-id>            # default: hourly
jabali dr feed --destination <dest-id> --cron "*/30 * * * *"   # tune freshness
```

`dr feed` ensures an enabled `system_backup` schedule ships to the DR destination.
It is idempotent — re-running just re-enables the existing schedule. Freshness of
the standby is bounded by this cadence (how often a new manifest appears) plus the
standby's 60s pull interval.

### On the standby — pair it

```
jabali dr pair --destination <dest-id> --peer-label <primary-hostname>
jabali dr status                                  # role=standby, destination set
```

Pairing flips `server_role` to `standby`. From this point:

- the drsync loop begins pulling: it lists the DR destination's `system_backup`
  manifests and, when a newer one exists than it last applied, runs
  `system.restore {apply:true, include_accounts:false}` (panel DB + config + TLS);
- the reconciler goes dormant (no serving config, no ACME certificate issuance);
- the API refuses tenant writes with `409 server_is_standby`;
- every page shows the read-only DR banner.

Each applied sync loads the PRIMARY's panel DB over this box — including
`server_settings` and `backup_destinations` — and then re-asserts the standby's
own identity (role, destination, peer label). From the first successful sync on,
the standby's destination row, credential env files, and `sso.key` are the
primary's replicated copies; that is expected, and it is why the same-name
destination prerequisite above matters. Admin logins on the standby also become
the PRIMARY's credentials (the identity DB replicates too).

Watch convergence with `jabali dr status` — `Last sync status` should reach `ok`
(applied a snapshot) or `current` (already newest). `waiting` means the primary
has not shipped a manifest yet; check `jabali dr feed` ran on the primary.

## Promotion (disaster)

Promotion is **irreversible** and the one action that can cause split-brain, so it
is confirm-gated and guarded.

**Before promoting, confirm the old primary is DOWN** — powered off or
network-isolated. Two live boxes serving the same domains is the exact failure this
design exists to avoid.

```
jabali dr promote
```

Sequence:

1. Refuses if this box is not a standby.
2. **Split-brain guard**: TCP-probes the peer (`dr_peer_label`, ports 443/22). If
   the old primary still answers, promotion is refused unless `--force`.
3. Interactive confirmation (`--yes` to script it).
4. **Final account-inclusive restore** from the DR destination:
   `system.restore {apply:true, include_accounts:true}` — brings home dirs, mail,
   and per-user databases online (the periodic sync deliberately skipped these).
   `--skip-restore` flips the role without this step.
5. **Role flip** to `primary`. This stops the drsync loop (it re-reads the role),
   lets writes through again, and wakes the reconciler.
6. Within ~1 minute the reconciler builds every domain's nginx vhost, DNS zone,
   and certificate from the replicated database.

Flags:

- `--force` — promote even if the old primary still answers (operator asserts it
  is isolated). Use only after confirming the old box is truly down.
- `--yes` — skip the interactive confirmation (scriptable).
- `--skip-restore` — role flip only, no final restore.

### Cut traffic over

The panel cannot move DNS you host elsewhere. After promotion:

- Point the domains' **A/AAAA** records (and **NS** if the zones are self-hosted)
  at the promoted box's IP.
- Point **MX** at the promoted box if it runs mail.
- Verify: `jabali dr status` shows `role: primary`; `systemctl status jabali-panel`
  is active; a test domain serves from the new box.

## Reverting a standby to primary (no disaster)

If you paired a box by mistake, or are decommissioning the standby:

```
jabali dr unpair        # role → primary, clears pairing; reconciler resumes
```

## Failure modes & checks

| Symptom | Check |
|---|---|
| `dr status` sync status `waiting` forever | `jabali dr feed` ran on the primary? Destination reachable from the standby? |
| `dr status` sync status `error` | `dr_last_sync_error` names it — usually destination unreachable or a version skew restore failure. |
| Standby accepts a write | It should 409. If not, confirm `server_role=standby` in `server_settings` and that `StandbyReadOnly` is wired (`panel-api/internal/app/app.go`). |
| Promotion refused "primary still answers" | The old primary is reachable on 443/22. Confirm it is down, or `--force` if you have isolated it. |
| Post-promote: domains 404 | Give the reconciler a tick, then `systemctl status jabali-panel`; force with the reconciler's periodic pass or restart the service. |

## Testing note

Steps 2–4 ship behind unit/integration coverage only: a real pull-restore +
promotion drill needs a second host, which the CI/testserver environment does not
have. Do a live drill on two throwaway VMs before relying on this in production —
in particular, verify the final account-inclusive restore and the DNS/MX cutover
against a real domain.
