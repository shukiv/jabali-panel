#!/bin/bash
# jabali-shell-bwrap — bubblewrap sandbox for Jabali Panel shell users
# Fallback when systemd-nspawn is unavailable (e.g., inside LXC containers).
#
# Provides:
#   - Mount namespace (user sees only their home + read-only system dirs)
#   - PID namespace (user can't see other users' processes)
#   - /tmp isolation (per-session tmpdir)
#
# Called from jabali-shell, which passes through $@ (e.g. -c "command")
set -euo pipefail

JUSER="$(whoami)"
HOME_DIR="/home/$JUSER"
UID_NUM="$(id -u)"
GID_NUM="$(id -g)"

# Detect remote command (login shell passes -c "cmd")
REMOTE_CMD=""
if [[ "${1:-}" == "-c" && -n "${2:-}" ]]; then
    REMOTE_CMD="$2"
fi

if [[ ! -d "$HOME_DIR" ]]; then
    echo "Error: Home directory $HOME_DIR does not exist." >&2
    exit 1
fi

# Build bwrap arguments:
#   - Read-only bind mounts for system directories
#   - Read-write bind for user home and /tmp
#   - New PID namespace (--unshare-pid) so user can't see other processes
#   - /dev from host (needed for /dev/null, /dev/tty, etc.)
#   - /proc mounted fresh inside the namespace
BWRAP_ARGS=(
    --die-with-parent

    # System dirs (read-only)
    --ro-bind /usr /usr
    --ro-bind /bin /bin
    --ro-bind /lib /lib
    --ro-bind /sbin /sbin
    --ro-bind /etc /etc

    # Device and proc
    --dev /dev
    --proc /proc

    # Isolated /tmp per session
    --tmpfs /tmp

    # User home (read-write)
    --bind "$HOME_DIR" "$HOME_DIR"

    # /run needs to exist for some tools, but isolated
    --tmpfs /run

    # PID namespace — user only sees their own processes
    --unshare-pid

    # Set UID/GID
    --uid "$UID_NUM"
    --gid "$GID_NUM"
)

# Optional system dirs (x86_64, multilib)
[[ -d /lib64 ]] && BWRAP_ARGS+=(--ro-bind /lib64 /lib64)
[[ -d /lib32 ]] && BWRAP_ARGS+=(--ro-bind /lib32 /lib32)

# Environment
ENV_ARGS=(
    --setenv HOME "$HOME_DIR"
    --setenv USER "$JUSER"
    --setenv SHELL /bin/bash
    --setenv PATH "/usr/local/bin:/usr/bin:/bin"
    --setenv LANG "${LANG:-C.UTF-8}"
)

if [[ -n "${TERM:-}" ]]; then
    ENV_ARGS+=(--setenv TERM "$TERM")
fi

# Command provided → run inside sandbox
if [[ -n "$REMOTE_CMD" ]]; then
    exec bwrap "${BWRAP_ARGS[@]}" "${ENV_ARGS[@]}" /bin/sh -c "$REMOTE_CMD"
fi

# No command → shell session
if tty -s 2>/dev/null; then
    # Interactive: user has a terminal
    exec bwrap "${BWRAP_ARGS[@]}" "${ENV_ARGS[@]}" /bin/bash -il
else
    # Non-interactive: VS Code, rsync, etc. — no prompt noise
    exec bwrap "${BWRAP_ARGS[@]}" "${ENV_ARGS[@]}" /bin/bash --login
fi
