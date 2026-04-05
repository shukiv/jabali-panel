#!/bin/bash
# jabali-shell — Login shell for Jabali Panel users
#
# Set as the user's login shell via chsh (not ForceCommand).
# This ensures clean stdout/stderr for tools like VS Code Remote SSH.
#
# Isolation modes (configured per-user via panel):
#   container — nsenter into nspawn container
#   sandbox   — bubblewrap sandbox (lighter, IDE-compatible)
#   standard  — plain bash shell, no isolation
#
# Invocation modes (handled by login shell convention):
#   Interactive:  ssh user@host        → $0 is a login shell, no args
#   Command:      ssh user@host "cmd"  → $0 -c "cmd"
#   SFTP/SCP:     handled by sshd directly (user is in sftpusers group with ChrootDirectory)

# No output before exec — VS Code probes depend on clean stdout
set -euo pipefail

JUSER="$(whoami)"
CONTAINER="${JUSER}-php"

# Detect if invoked as login shell with -c "command" (ssh user@host "cmd")
REMOTE_CMD=""
if [[ "${1:-}" == "-c" && -n "${2:-}" ]]; then
    REMOTE_CMD="$2"
fi

# Read configured isolation mode (default: standard)
SHELL_MODE="standard"
MODE_FILE="${HOME}/.jabali-shell-mode"
if [[ -r "$MODE_FILE" ]]; then
    SHELL_MODE="$(head -1 "$MODE_FILE" | tr -d '[:space:]')"
fi

# Validate mode
case "$SHELL_MODE" in
    container|sandbox|standard) ;;
    *) SHELL_MODE="standard" ;;
esac

# --- nspawn container ---
try_nspawn() {
    command -v machinectl &>/dev/null || return 1

    # Ensure container is running
    if ! machinectl list --no-legend 2>/dev/null | grep -q "^${CONTAINER} "; then
        if command -v jabali-isolate &>/dev/null; then
            if [[ ! -d "/var/lib/machines/${CONTAINER}" ]]; then
                sudo /usr/local/bin/jabali-isolate create "$JUSER" >/dev/null 2>&1 || return 1
            fi
            sudo /usr/local/bin/jabali-isolate start "$JUSER" >/dev/null 2>&1 || return 1
            sleep 1
        else
            return 1
        fi

        if ! machinectl list --no-legend 2>/dev/null | grep -q "^${CONTAINER} "; then
            return 1
        fi
    fi

    local leader
    leader=$(machinectl show "$CONTAINER" --property=Leader --value 2>/dev/null)
    if [[ -z "$leader" || "$leader" == "0" ]]; then
        return 1
    fi

    local uid_num gid_num
    uid_num="$(id -u)"
    gid_num="$(id -g)"

    if [[ -z "$REMOTE_CMD" ]]; then
        exec sudo nsenter --target "$leader" --mount --pid --ipc --uts --no-fork \
            setpriv --reuid="$uid_num" --regid="$gid_num" --init-groups \
            env HOME="/home/$JUSER" USER="$JUSER" SHELL=/bin/bash TERM="${TERM:-xterm-256color}" \
            /bin/bash -il
    fi

    exec sudo nsenter --target "$leader" --mount --pid --ipc --uts \
        setpriv --reuid="$uid_num" --regid="$gid_num" --init-groups \
        env HOME="/home/$JUSER" USER="$JUSER" SHELL=/bin/bash \
        /bin/sh -c "$REMOTE_CMD"
}

# --- bubblewrap sandbox ---
try_bwrap() {
    command -v bwrap &>/dev/null || return 1

    local bwrap_shell="/usr/local/bin/jabali-shell-bwrap"
    if [[ -x "$bwrap_shell" ]]; then
        exec "$bwrap_shell"
    fi

    return 1
}

# --- standard shell (no isolation) ---
try_standard() {
    if [[ -z "$REMOTE_CMD" ]]; then
        exec /bin/bash -il
    fi

    exec /bin/sh -c "$REMOTE_CMD"
}

# --- Main: dispatch based on configured mode ---
case "$SHELL_MODE" in
    container)
        if try_nspawn; then exit 0; fi
        if try_bwrap; then exit 0; fi
        echo "Error: Container isolation unavailable. Contact your administrator." >&2
        exit 1
        ;;
    sandbox)
        if try_bwrap; then exit 0; fi
        echo "Error: Sandbox isolation unavailable. Contact your administrator." >&2
        exit 1
        ;;
    standard)
        try_standard
        ;;
esac
