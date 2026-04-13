# ADR-0002: Restic as single source of truth, no panel DB tables

**Date**: 2026-04-06
**Status**: accepted
**Deciders**: shuki, claude

## Context

The panel needs to display backup state: which users have snapshots, when they were taken, what they contain. A common approach is to mirror this state into the panel's database tables. However, jabali-backup is a standalone addon that should not modify the Jabali panel's database schema — no migrations, no models, no Eloquent tables.

Restic already stores all backup metadata (snapshots, tags, timestamps, paths) in its repository. Duplicating this into MySQL adds sync complexity and a second source of truth that can drift.

## Decision

Use restic's own repository as the single source of truth for all backup state. The panel queries restic directly (via the agent and CLI) instead of maintaining its own database tables.

- `restic snapshots --json` provides the snapshot list with dates and tags.
- `restic ls` provides file listings within a snapshot.
- `restic dump` retrieves metadata files (e.g., `account.json`) from snapshots.
- The agent exposes these as RPC calls (`jb.snapshots`, `jb.snapshot_inventory`, etc.).

## Alternatives Considered

### Alternative 1: Panel database tables mirroring restic state
- **Pros**: Fast queries, Filament model integration, familiar Eloquent patterns
- **Cons**: Requires migrations in the Jabali panel DB, sync jobs to keep tables current, two sources of truth
- **Why not**: jabali-backup must not touch the panel's database schema; addon should be removable without DB cleanup

### Alternative 2: SQLite sidecar database
- **Pros**: No panel DB modification, local to the addon
- **Cons**: Still a second source of truth, sync complexity remains, extra file to manage
- **Why not**: Restic already has the data; a sidecar just adds maintenance burden

## Consequences

### Positive
- Zero database modifications to the Jabali panel — clean install/uninstall
- No sync drift — restic is always authoritative
- Simpler addon architecture — fewer moving parts
- Backup state is always consistent with actual repository contents

### Negative
- Every panel page load that shows backup data requires an agent RPC call to query restic
- Restic CLI calls are slower than database queries (typically 200-500ms per call)
- Cannot use Filament's Eloquent table features (pagination, sorting, filtering) natively — must build arrays manually

### Risks
- Performance on panels with many users/snapshots — mitigated by caching snapshot lists in the agent with short TTL
- Agent unavailability blocks the panel — mitigated by graceful error handling and retry in the Filament page
