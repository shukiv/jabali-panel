#!/bin/bash
# jabali-shell — SSH login shell for Jabali Panel users
#
# Isolation modes (configured per-user via panel):
#   container — full nspawn container via jabali-isolator
#   sandbox   — bubblewrap sandbox (lighter, IDE-compatible)
#   standard  — plain bash shell, no isolation
#
# Three modes per tier:
#   1. Interactive SSH login: ssh user@host → bash inside sandbox
#   2. Command execution: ssh user@host "cmd" → run inside sandbox
#   3. SFTP/SCP/rsync: handled transparently
set -euo pipefail

# Suppress stderr during shell startup for non-interactive sessions (e.g. VS Code Remote SSH).
# Bash startup messages on stderr cause VS Code to misdetect the platform as "windows".
# stderr is restored before exec-ing into the actual shell/container.
if [[ -n "${SSH_ORIGINAL_COMMAND:-}" ]] || ! tty -s 2>/dev/null; then
    exec 3>&2 2>/dev/null
    trap 'exec 2>&3 3>&-' EXIT
fi

JUSER="$(whoami)"
CONTAINER="${JUSER}-php"

# Handle SFTP/SCP — pass through directly without isolation
# ForceCommand overrides subsystem requests, so we detect and proxy them
if [[ "${SSH_ORIGINAL_COMMAND:-}" == *"sftp-server"* ]] || [[ "${SSH_ORIGINAL_COMMAND:-}" == "/usr/lib/openssh/sftp-server" ]]; then
    exec /usr/lib/openssh/sftp-server
fi
if [[ "${SSH_ORIGINAL_COMMAND:-}" == "scp "* ]]; then
    exec /bin/sh -c "$SSH_ORIGINAL_COMMAND"
fi

# Read configured isolation mode (default: container)
SHELL_MODE="container"
MODE_FILE="${HOME}/.jabali-shell-mode"
if [[ -r "$MODE_FILE" ]]; then
    SHELL_MODE="$(head -1 "$MODE_FILE" | tr -d '[:space:]')"
fi

# Validate mode
case "$SHELL_MODE" in
    container|sandbox|standard) ;;
    *) SHELL_MODE="container" ;;
esac

# --- Tier 1: nspawn container ---
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

    if [[ -z "${SSH_ORIGINAL_COMMAND:-}" ]]; then
        exec sudo nsenter --target "$leader" --mount --pid --ipc --uts --no-fork \
            setpriv --reuid="$uid_num" --regid="$gid_num" --init-groups \
            env HOME="/home/$JUSER" USER="$JUSER" SHELL=/bin/bash TERM="${TERM:-xterm-256color}" \
            /bin/bash -il
    fi

    exec sudo nsenter --target "$leader" --mount --pid --ipc --uts \
        setpriv --reuid="$uid_num" --regid="$gid_num" --init-groups \
        env HOME="/home/$JUSER" USER="$JUSER" SHELL=/bin/bash \
        /bin/sh -c "$SSH_ORIGINAL_COMMAND"
}

# --- Tier 2: bubblewrap sandbox ---
try_bwrap() {
    command -v bwrap &>/dev/null || return 1

    local bwrap_shell="/usr/local/bin/jabali-shell-bwrap"
    if [[ -x "$bwrap_shell" ]]; then
        exec "$bwrap_shell"
    fi

    return 1
}

# --- Tier 3: standard shell (no isolation) ---
try_standard() {
    if [[ -z "${SSH_ORIGINAL_COMMAND:-}" ]]; then
        exec /bin/bash -il
    fi

    exec /bin/sh -c "$SSH_ORIGINAL_COMMAND"
}

# --- Main: dispatch based on configured mode ---
case "$SHELL_MODE" in
    container)
        if try_nspawn; then exit 0; fi
        if try_bwrap; then exit 0; fi
        echo "Error: Container isolation unavailable (nspawn and bwrap both failed). Contact your administrator." >&2
        exit 1
        ;;
    sandbox)
        if try_bwrap; then exit 0; fi
        echo "Error: Sandbox isolation unavailable (bwrap not found). Contact your administrator." >&2
        exit 1
        ;;
    standard)
        try_standard
        ;;
esac
