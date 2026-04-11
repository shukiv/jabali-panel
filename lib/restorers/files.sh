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

    # Fix ownership and web server permissions
    _fix_home_permissions "$username" "$target"

    local file_count
    file_count=$(find "$target" -type f 2>/dev/null | wc -l)
    log_info "restore/files: Restored $file_count files to $target"
}

# Set correct ownership and permissions for a Jabali hosting account home dir.
# nginx runs as www-data and needs to traverse into the home dir to serve files.
_fix_home_permissions() {
    local username="$1" home="$2"

    if ! id "$username" &>/dev/null; then
        log_warn "restore/files: System user $username doesn't exist, permissions not fixed"
        return 0
    fi

    # 1. Ownership: everything belongs to user:user
    chown -R "${username}:${username}" "$home"

    # 2. Home dir: 751 — owner full, group r+x, other execute-only (nginx traverse)
    chmod 751 "$home"

    # 3. Domains tree: nginx needs to read document roots
    if [[ -d "${home}/domains" ]]; then
        # Domain dirs and subdirs: 755 so nginx can traverse + read
        find "${home}/domains" -type d -exec chmod 755 {} +
        # Files: 644 (owner write, world read) — standard for web content
        find "${home}/domains" -type f -exec chmod 644 {} +
    fi

    # 4. Nginx cache dir: needs to be writable by nginx (www-data via FPM pool)
    if [[ -d "${home}/cache" ]]; then
        chmod -R o+rX "${home}/cache"
    fi

    # 5. SSH: strict permissions required by sshd
    if [[ -d "${home}/.ssh" ]]; then
        chmod 700 "${home}/.ssh"
        [[ -f "${home}/.ssh/authorized_keys" ]] && chmod 600 "${home}/.ssh/authorized_keys"
    fi

    # 6. Private dirs: no world access
    for dir in backups tmp .composer .npm; do
        [[ -d "${home}/${dir}" ]] && chmod 750 "${home}/${dir}"
    done

    log_info "restore/files: Fixed ownership and web server permissions for $home"
}
