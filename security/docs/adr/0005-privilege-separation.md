# ADR-0005: Privilege Separation via Dedicated System User

**Date**: 2026-04-10
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

The daemon was running as root, giving it unrestricted access to the entire system. While convenient (no permission issues), this violates the principle of least privilege. A vulnerability in the daemon or API could lead to full root compromise. Most operations (file scanning, inotify watching) only need targeted capabilities, not full root. A few operations (systemctl restart, ufw, nginx reload) genuinely need elevated privileges but can be whitelisted.

## Decision

Run the daemon as a dedicated `jabali-security` system user with Linux capabilities (CAP_DAC_READ_SEARCH, CAP_DAC_OVERRIDE, CAP_NET_ADMIN, CAP_KILL, CAP_FOWNER, CAP_SETUID, CAP_SETGID, CAP_AUDIT_WRITE) and a sudoers whitelist (`/etc/sudoers.d/jabali-security`) for operations that require root. The `lib/privilege.py` module provides `sudo_prefix()` and `sudo_cmd()` helpers that return empty lists when running as root (backward compatible) or `['/usr/bin/sudo']` when running as the service user. The `update` command automatically migrates existing root-based installations.

## Alternatives Considered

### Alternative 1: Continue running as root
- **Pros**: Simple, no permission issues, no migration needed
- **Cons**: Full root compromise if daemon is exploited, violates least privilege
- **Why not**: Unacceptable security posture for a security product

### Alternative 2: Capabilities only, no sudo
- **Pros**: Cleaner (no setuid binaries), tighter security
- **Cons**: Cannot call ufw, systemctl, nginx, or cscli — these require real root or sudo
- **Why not**: Too many privileged CLI tools need root; capabilities alone are insufficient

### Alternative 3: Separate privileged helper daemon
- **Pros**: Complete isolation between scanning and privileged operations
- **Cons**: Complex IPC, two services to manage, increased attack surface from IPC channel
- **Why not**: Over-engineered for the use case; sudoers whitelist achieves the same goal with less complexity

## Consequences

### Positive
- Daemon compromise no longer grants full root access
- Sudoers whitelist explicitly enumerates every privileged operation
- Backward compatible — existing root installations keep working via empty sudo_prefix()
- Automatic migration on `jabali-security update`

### Negative
- NoNewPrivileges=no required (sudo is a setuid binary), weakening one systemd hardening option
- ProtectSystem=full instead of strict (sudo needs /run/sudo and /var/lib/sudo)
- More capabilities in CapabilityBoundingSet (CAP_SETUID/SETGID/AUDIT_WRITE for sudo)

### Risks
- Sudoers misconfiguration could grant broader access than intended — mitigated by `visudo -cf` validation during install and narrow command patterns in the whitelist
