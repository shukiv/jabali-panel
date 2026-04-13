#!/usr/bin/env bash
# Structured logging with levels and timestamps

declare -A LOG_LEVELS=([debug]=0 [info]=1 [warn]=2 [error]=3)
LOG_LEVEL="${LOG_LEVEL:-info}"
LOG_FILE="${LOG_FILE:-/var/log/jabali-backup.log}"

_log() {
    local level="$1"; shift
    local current="${LOG_LEVELS[${LOG_LEVEL}]:-1}"
    local target="${LOG_LEVELS[${level}]:-1}"
    [[ "$target" -lt "$current" ]] && return
    local ts
    ts="$(date '+%Y-%m-%d %H:%M:%S')"
    local msg="[$ts] [${level^^}] $*"
    echo "$msg" >&2
    if [[ -n "$LOG_FILE" ]] && [[ -w "$(dirname "$LOG_FILE")" || -w "$LOG_FILE" ]]; then
        echo "$msg" >> "$LOG_FILE"
    fi
}

log_debug() { _log debug "$@"; }
log_info()  { _log info  "$@"; }
log_warn()  { _log warn  "$@"; }
log_error() { _log error "$@"; }
