# ADR-0006: Per-user resource limiting via cgroups v2 systemd slices

**Date**: 2026-04-09
**Status**: accepted
**Deciders**: Shuki

## Context

Jabali Panel needed per-user resource control (CPU, memory, I/O, processes) to prevent individual users from consuming all server resources. The panel already had disk quotas via Linux quota tools and PHP-FPM pool limits, but no kernel-level enforcement for CPU, memory, or I/O.

## Decision

Use cgroups v2 with systemd slices. Each user gets a persistent slice file at `/etc/systemd/system/jabali-user-{username}.slice` under a parent `jabali.slice`. PHP-FPM workers are moved into the slice by the agent and health monitor. SSH sessions move themselves on login. Nginx rate/connection limiting added as a complementary layer.

## Alternatives Considered

### Alternative 1: Per-user systemd services for PHP-FPM
- **Pros**: Each pool is its own service with native cgroup
- **Cons**: PHP-FPM runs as one service with multiple pools — splitting requires major refactoring
- **Why not**: Would need to rewrite FPM pool management to use separate systemd units per user

### Alternative 2: Docker/LXC containers per user
- **Pros**: Full isolation including filesystem, network
- **Cons**: Heavy resource overhead, complex networking, duplicated services per user
- **Why not**: jabali-isolator already provides nspawn containers for SSH; adding container-based FPM would double complexity

### Alternative 3: ulimits via PAM/limits.conf
- **Pros**: Simple, no systemd dependency
- **Cons**: Only applies at login (not FPM), no I/O limiting, coarse-grained CPU control
- **Why not**: Doesn't cover the main workload (PHP-FPM workers)

## Consequences

### Positive
- Kernel-enforced limits — users cannot bypass
- Zero overhead when under limits
- Familiar systemd tooling for diagnostics (`systemctl status`, `systemd-cgtop`)
- Health monitor reassigns respawned FPM workers automatically

### Negative
- Requires cgroups v2 (Ubuntu 22.04+/Debian 12+)
- FPM workers must be periodically moved into slices (not native)
- I/O limits require identifying the root block device

### Risks
- LXC containers may not support cgroups v2 nesting — mitigated by graceful fallback (limits stored in DB but not enforced)
