# ADR-0007: Delegate infrastructure recreation to the agent, don't restore raw configs

**Date**: 2026-04-12
**Status**: accepted (partial — applies to nginx cache zones; broader rollout pending)
**Deciders**: shuki, claude, panel team

## Context

During restore testing, the panel team identified a class of bug where restored
configuration files conflict with infrastructure the agent manages. The specific case:

- Backup captures `shuki.cache-zone.conf` from the old Jabali layout
  (`/etc/nginx/sites-enabled/{user}.cache-zone.conf`).
- Agent now manages cache zones at a new path
  (`/etc/nginx/jabali/cache-zones/{user}.conf`).
- Restoring the old file alongside the agent-created new file produces duplicate
  `fastcgi_cache_path` directives for the same directory. Nginx `-t` fails, blocking all
  domain operations until the duplicate is manually removed.

This pattern will recur whenever the panel's infrastructure layout evolves:
nginx includes, PHP-FPM pool locations, agent addon structure, etc. The fundamental issue
is that backups capture a point-in-time config format that may no longer match the
current runtime format.

Panel team's recommendation:

> Instead of restoring raw config files (nginx vhosts, FPM pools, cache zones),
> the restore should:
> 1. Restore the data (home directory files, database dumps, panel DB records)
> 2. Call the agent to recreate the infrastructure (vhost, FPM pool, cache zone, MySQL users, Stalwart accounts)

## Decision

For config files where the agent has a clear recreation API, delegate to the agent rather
than restoring the raw file. Apply this immediately to nginx cache zones; expand to other
infrastructure (vhosts, FPM pools) as the agent exposes recreate endpoints.

Current state:
- **Cache zones**: skipped by restorer; agent creates them at the current path when
  domains are set up.
- **Nginx vhosts**: still restored raw, but post-process to fix known path drifts
  (FPM socket path — see `lib/restorers/nginx.sh`).
- **PHP-FPM pools, MySQL users, Stalwart accounts**: still restored raw; migration
  deferred until agent exposes recreation endpoints.

## Alternatives Considered

### Alternative 1: Keep restoring raw configs, add version tags to snapshots
- **Pros**: Simple, no agent dependency for restore
- **Cons**: Restore logic needs to handle every layout version in the backup history;
  complexity grows with every agent release
- **Why not**: Shifts complexity to the restorer, where it fans out across all config types

### Alternative 2: Translate old configs to new format during restore
- **Pros**: Preserves everything from the backup
- **Cons**: Requires maintaining format-translation code for every config drift; fragile
- **Why not**: Duplicates knowledge the agent already has

### Alternative 3: Don't restore configs at all; agent rebuilds everything from panel DB
- **Pros**: Cleanest separation — backups are just data + panel DB; configs are derived
- **Cons**: Requires agent to expose full recreation API for all infrastructure;
  not all of that API exists yet
- **Why not**: Right direction but too large a step in one release; incremental migration
  is safer

## Consequences

### Positive
- Restored systems use current infrastructure layout, not stale layout from backup time
- No duplicate-config conflicts when the agent evolves paths/includes
- Backup size decreases (fewer config files to capture) as migration progresses
- Restore is more robust to version drift between backup and restore time

### Negative
- Restore depends on the agent being installed and functional (already true for MySQL/DNS/SSL)
- If the agent's recreation API has a bug, restore loses access to the original config as a fallback
- Gradual migration means the restorer has to document which components are agent-managed
  vs still raw-restored

### Risks
- Agent recreation API may not support every edge case captured in old raw configs (e.g.,
  custom directives, hotlink rules) — mitigated by keeping hotlink settings in the panel DB
  (already backed up via `metadata` collector) so the agent can regenerate them.
- Users on older agent versions may not have the recreation endpoints — mitigated by
  checking agent capability before skipping raw restore.

## Migration Plan

| Component | Current | Target | Blocker |
|-----------|---------|--------|---------|
| Nginx cache zone | Skipped, agent creates | Skipped, agent creates | Done (ADR-0007) |
| Nginx vhost | Raw restore + socket-path rewrite | Agent `createVhost(domain)` | Agent endpoint needed |
| PHP-FPM pool | Raw restore | Agent `createPool(user, version)` | Agent endpoint needed |
| MySQL user | Raw CREATE USER + password sync | Agent `createMysqlUser(user, password)` | Agent endpoint needed |
| Stalwart account | stalwart-cli import | Agent `createStalwartAccount(user)` | Agent endpoint exists, needs wiring |
