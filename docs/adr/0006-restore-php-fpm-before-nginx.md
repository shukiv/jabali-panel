# ADR-0006: Restore PHP-FPM pool before nginx vhost

**Date**: 2026-04-12
**Status**: accepted
**Deciders**: shuki, claude, panel team

## Context

The restore pipeline originally ordered steps as: metadata → files → dns → ssl → email →
nginx → php → mysql. When nginx restore ran, it reloaded the vhost pointing to
`/run/php/php8.5-fpm-{user}.sock`, but that socket did not exist yet because the PHP-FPM
pool was restored in the next step. Nginx `-t` passed (syntax check), but every request
returned 502 Bad Gateway.

The panel team flagged this ordering issue during review of the restore flow, noting that
restoring nginx before PHP-FPM is a dependency violation.

## Decision

Swap restore order so PHP-FPM pool is restored before nginx:
`metadata → files → dns → ssl → email → php → nginx → mysql → postgres → cron → redis → stalwart`.

This ensures the user-specific FPM socket exists and is listening before nginx reloads
and starts routing requests to it.

## Alternatives Considered

### Alternative 1: Defer nginx reload until after PHP-FPM
- **Pros**: Preserves the original step order
- **Cons**: Requires tracking "needs reload" state across restorers; still fails if another restorer reloads nginx earlier
- **Why not**: Order dependency is a strong reason to encode in step sequence, not a runtime flag

### Alternative 2: Restore nginx with generic `fastcgi_pass`, swap socket later
- **Pros**: Nginx works immediately
- **Cons**: Requires a second nginx reload; any request during the window between reloads hits the wrong socket
- **Why not**: More moving parts, harder to reason about, still has a failure window

### Alternative 3: Start FPM socket separately before nginx
- **Pros**: No config reordering
- **Cons**: Implicit dependency between restorers; breaks the principle that each restorer owns its domain
- **Why not**: Violates restorer independence

## Consequences

### Positive
- FPM socket exists before any nginx request tries to use it
- No 502 errors during restore
- Matches the natural dependency order (producer before consumer)
- Same ordering applied to both full and component-based restore paths

### Negative
- Deviates from a reader's expectation that nginx is restored with other web configs
- Requires care when adding new restorers — they must respect this ordering

### Risks
- Future collectors added out of order could reintroduce this bug — mitigated by a comment
  in `bin/jabali-backup` documenting the dependency between steps 7 and 8
