# ADR-0005: Agent Addon Route Loading from /etc/jabali/agent.d/

**Date**: 2026-04-05
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

The jabali-agent is a single PHP file (~24k lines) with a `match` statement routing actions to handler functions. External tools (jabali-backup, jabali-security) need to add their own agent routes so the panel can call their CLI commands through the agent's Unix socket.

Previously, this required patching the agent file directly — fragile, creates merge conflicts on updates, and couples external tools to the agent's internal structure.

## Decision

The agent's `handleAction()` falls through to `handleAddonAction()` for unknown actions. This function loads all PHP files from `/etc/jabali/agent.d/`, each returning an associative array mapping action names to callable handlers. Routes are cached in a static variable (loaded once per process).

```php
// /etc/jabali/agent.d/jabali-backup.php
return [
    'backup.list_snapshots' => fn(array $p) => backupListSnapshots($p),
    'backup.run' => fn(array $p) => backupRun($p),
];
```

## Alternatives Considered

### Alternative 1: Patch the agent's match statement directly
- **Pros**: Simple, no new mechanism needed
- **Cons**: Fragile, merge conflicts on every update, couples tools to agent internals
- **Why not**: Doesn't scale to multiple independent tools

### Alternative 2: Separate agent per tool (each with its own socket)
- **Pros**: Complete isolation, independent deployment
- **Cons**: Multiple sockets, multiple services, panel needs to know which agent handles which action
- **Why not**: Over-engineered for the use case. The panel expects one agent endpoint.

### Alternative 3: HTTP API instead of Unix socket
- **Pros**: Standard protocol, easy to extend with middleware
- **Cons**: Network exposure, authentication overhead, breaks the current AgentClient
- **Why not**: Unix socket is secure by design (filesystem permissions). HTTP adds attack surface.

## Consequences

### Positive
- External tools register routes without touching the agent codebase
- Clean separation — each addon is a single PHP file
- Core routes take priority (addons only handle unmatched actions)
- Routes loaded once and cached (no per-request overhead)
- Install/uninstall is just adding/removing a file

### Negative
- No dependency ordering between addons
- No namespace isolation — addon functions share the global scope
- Addon errors can crash the agent process

### Risks
- Malicious addon file could compromise the agent (runs as root). Mitigation: `/etc/jabali/agent.d/` is root-owned, only root can write to it. Addons are installed by trusted package installers.
