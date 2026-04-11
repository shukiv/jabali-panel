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
# SFTP chroot requires: home dir owned by root:{user} with 0750.
# nginx (www-data) traverses into domains/ via the group or ACL.
_fix_home_permissions() {
    local username="$1" home="$2"

    if ! id "$username" &>/dev/null; then
        log_warn "restore/files: System user $username doesn't exist, permissions not fixed"
        return 0
    fi

    # 1. All content owned by user:user
    chown -R "${username}:${username}" "$home"

    # 2. Home dir: root:{user} 0750 — SFTP chroot requirement
    #    sshd requires the chroot dir to be owned by root with no group/other write
    chown "root:${username}" "$home"
    chmod 0750 "$home"

    # 3. Domains tree: nginx (www-data) needs read access via ACL
    if [[ -d "${home}/domains" ]]; then
        # Directories: 750 (owner rwx, group r-x) — group = user
        find "${home}/domains" -type d -exec chmod 750 {} +
        # Files: 640 (owner rw, group r) — standard for web content
        find "${home}/domains" -type f -exec chmod 640 {} +
        # Grant www-data read+traverse access via POSIX ACL
        if command -v setfacl &>/dev/null; then
            setfacl -R -m u:www-data:rX "${home}/domains" 2>/dev/null || true
            setfacl -R -d -m u:www-data:rX "${home}/domains" 2>/dev/null || true
        fi
    fi

    # 4. Nginx cache dir: writable by www-data (FPM pool runs as user, nginx writes cache)
    if [[ -d "${home}/cache" ]]; then
        chown -R "${username}:${username}" "${home}/cache"
        chmod -R 750 "${home}/cache"
        if command -v setfacl &>/dev/null; then
            setfacl -R -m u:www-data:rwX "${home}/cache" 2>/dev/null || true
            setfacl -R -d -m u:www-data:rwX "${home}/cache" 2>/dev/null || true
        fi
    fi

    # 5. SSH: strict permissions required by sshd
    if [[ -d "${home}/.ssh" ]]; then
        chmod 700 "${home}/.ssh"
        chown "${username}:${username}" "${home}/.ssh"
        [[ -f "${home}/.ssh/authorized_keys" ]] && chmod 600 "${home}/.ssh/authorized_keys"
    fi

    # 6. Private dirs: no world access
    for dir in backups tmp .composer .npm .wp-cli; do
        [[ -d "${home}/${dir}" ]] && chmod 750 "${home}/${dir}"
    done

    # 7. Dotfiles: readable by user only
    for dotfile in .domains .wordpress_sites .redis_credentials; do
        [[ -f "${home}/${dotfile}" ]] && chmod 600 "${home}/${dotfile}"
    done

    log_info "restore/files: Fixed ownership and permissions for $home (root:${username} 0750)"
}
