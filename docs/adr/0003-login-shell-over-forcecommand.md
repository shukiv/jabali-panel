# ADR-0003: Login Shell over ForceCommand for SSH Isolation

**Date**: 2026-04-05
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

Shell users had `ForceCommand /usr/local/bin/jabali-shell` in sshd_config's `Match Group shellusers` block. This intercepted all SSH sessions and dispatched to nspawn containers, bubblewrap sandboxes, or plain bash. However, VS Code Remote SSH sends `uname` probes to detect the remote platform, and ForceCommand intercepted these too. The shell wrapper's startup noise (prompts, locale warnings, container bootstrap messages) caused VS Code to misdetect the platform as "windows" and attempt to run PowerShell, failing the connection entirely.

cPanel uses a login shell (`/usr/local/cpanel/bin/jailshell`) via `chsh`, not ForceCommand. Plesk uses standard `/bin/bash`. ForceCommand is designed for locked-down command execution, not interactive development environments.

## Decision

Set `jabali-shell` as the user's login shell via `chsh -s /usr/local/bin/jabali-shell` instead of using ForceCommand in sshd_config. The shellusers Match block retains `AllowTcpForwarding yes` and `X11Forwarding no` but no longer has ForceCommand.

## Alternatives Considered

### Alternative 1: Suppress stderr during shell startup
- **Pros**: Quick fix, no architecture change
- **Cons**: Hides real errors, fragile, VS Code can change probe behavior anytime
- **Why not**: Band-aid that masks the symptom without fixing the root cause

### Alternative 2: Detect VS Code probes in jabali-shell and pass through
- **Pros**: Keeps ForceCommand, targeted fix
- **Cons**: Brittle, must track VS Code's evolving probe commands, breaks with other IDE tools
- **Why not**: Chasing edge cases forever

### Alternative 3: Match User blocks per user instead of Match Group
- **Pros**: Reduces blast radius
- **Cons**: Still uses ForceCommand, same VS Code incompatibility
- **Why not**: Doesn't solve the core problem

## Consequences

### Positive
- VS Code Remote SSH works out of the box
- All SSH tools (rsync, scp, IDE integrations) work without special handling
- Cleaner architecture — login shell is the standard hosting panel approach
- SFTP handled separately by sftpusers group with ChrootDirectory (unchanged)

### Negative
- Users with `chsh` access could theoretically change their shell — mitigated by hosting users not having sudo or chsh access
- `/usr/local/bin/jabali-shell` must be in `/etc/shells` for SSH to accept it, which is a requirement

### Risks
- If a user somehow gains `chsh` access, they could bypass isolation. Mitigation: hosting users don't have sudo, and `chsh` requires the current password plus the shell must be in `/etc/shells`.
