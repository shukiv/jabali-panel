#!/usr/bin/env bash
# Restorer: home directory files

restore_files() {
    local extract_dir="$1" username="$2" force="${3:-0}"
    local source="${extract_dir}/home/${username}"
    local target="/home/${username}"

    if [[ ! -d "$source" ]]; then
        log_warn "restore/files: No home directory found in snapshot for $username"
        return 0
    fi

    if [[ -d "$target" ]] && [[ "$force" -eq 0 ]] && [[ "${RESTORE_USER_CREATED:-0}" -ne 1 ]]; then
        # Check if non-empty — skip check when user was just created by metadata restorer
        if [[ -n "$(ls -A "$target" 2>/dev/null)" ]]; then
            log_info "restore/files: $target exists and is non-empty, skipping (use --force to overwrite)"
            return 0
        fi
    fi

    mkdir -p "$target"

    log_info "restore/files: Syncing $source → $target"
    rsync -a --delete "$source/" "$target/"

    # Fix ownership
    if id "$username" &>/dev/null; then
        chown -R "${username}:${username}" "$target"
        log_info "restore/files: Fixed ownership for $target"
    else
        log_warn "restore/files: System user $username doesn't exist, ownership not fixed"
    fi

    local file_count
    file_count=$(find "$target" -type f 2>/dev/null | wc -l)
    log_info "restore/files: Restored $file_count files to $target"
}
