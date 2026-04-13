# ADR-0003: Unix Socket API Instead of TCP Port

**Date**: 2026-04-02
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

The jabali-security daemon exposes a REST API for the CLI, panel plugin, and management tools. Originally bound to `127.0.0.1:9876` (TCP). On shared hosting servers, any local user can connect to a TCP port on localhost, and port conflicts with other services are common.

## Decision

Bind the API to a Unix socket at `/run/jabali-security/jabali-security.sock` with permissions `0660 root:www-data`. TCP binding is disabled by default.

## Alternatives Considered

### Alternative 1: TCP on localhost (original)
- **Pros**: Simple, works with curl/browsers, easy debugging
- **Cons**: Any local user can connect, port 9876 may conflict, no filesystem-level ACL
- **Why not**: Insufficient access control on a multi-tenant shared hosting server

### Alternative 2: TCP with mTLS
- **Pros**: Strong authentication, works across network
- **Cons**: Certificate management complexity, overkill for local-only daemon
- **Why not**: The API never needs to be network-accessible; Unix socket is simpler

## Consequences

### Positive
- Access control via filesystem permissions (only root and www-data group can connect)
- No port conflicts with other services
- No network exposure by default — cannot be reached from outside the server
- Panel plugin connects via PHP stream wrapper (`unix:///run/jabali-security/jabali-security.sock`)

### Negative
- Cannot use browser to test API directly (need `curl --unix-socket`)
- Remote management requires SSH tunnel or a proxy

### Risks
- Socket file permissions must be correct after daemon restart — mitigated by systemd `RuntimeDirectory=jabali-security` which creates the directory with correct ownership
