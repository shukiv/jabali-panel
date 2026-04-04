# Move SSH Management from Security to Panel

## Summary
Move SSH port, password auth, and per-user shell management from jabali-security to the panel's Server Settings General tab. The hosting package `ssh_shell_enabled` toggle becomes the source of truth for shell access.

## Steps

1. Add SSH Settings section to Server Settings General tab (port, password auth)
2. Remove security config check from agent's sshEnableShell()
3. Add per-user shell enable/disable to admin Users table
4. Update shared docs for security team to remove SSH Jail tab
