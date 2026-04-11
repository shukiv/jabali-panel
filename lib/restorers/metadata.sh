#!/usr/bin/env bash
# Restorer: account metadata (user, domains, aliases)
# MUST run first — creates FK targets for other restorers
#
# NOTE: Creates users and domains directly (useradd + DB) instead of via
# `jabali` CLI, because restore runs inside the agent and `jabali` CLI
# calls the agent — causing a deadlock on the socket.

restore_metadata() {
    local extract_dir="$1" username="$2" force="${3:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/metadata"
    local account_json="${staging}/account.json"

    if [[ ! -f "$account_json" ]]; then
        log_warn "restore/metadata: No account.json found, skipping"
        return 0
    fi

    log_info "restore/metadata: Restoring account metadata for $username"

    # Parse account.json fields
    local user_email
    user_email=$(_json_val "$account_json" '"email"' | _strip_json_str)

    # Check if user exists
    local existing_id
    existing_id=$(_db_query "SELECT id FROM users WHERE username='$(mysql_escape "$username")' LIMIT 1")

    if [[ -n "$existing_id" ]]; then
        RESTORE_USER_ID="$existing_id"
        RESTORE_USER_CREATED=0
        log_info "restore/metadata: User $username exists (id=$existing_id)"
    else
        log_info "restore/metadata: Creating user $username"

        # Create Linux system user
        if ! id "$username" &>/dev/null; then
            # Try to restore original UID/GID if available in system_user.json
            local sys_user_json="${staging}/system_user.json"
            local useradd_opts=(-m -d "/home/${username}" -s /usr/sbin/nologin)
            if [[ -f "$sys_user_json" ]]; then
                local orig_uid orig_gid orig_shell
                orig_uid=$(grep -oP '"uid"\s*:\s*\K[0-9]+' "$sys_user_json" 2>/dev/null || true)
                orig_gid=$(grep -oP '"gid"\s*:\s*\K[0-9]+' "$sys_user_json" 2>/dev/null || true)
                orig_shell=$(grep -oP '"shell"\s*:\s*"\K[^"]+' "$sys_user_json" 2>/dev/null || true)
                # Use original UID/GID if not already taken
                if [[ -n "$orig_uid" ]] && ! getent passwd "$orig_uid" &>/dev/null; then
                    useradd_opts+=(-u "$orig_uid")
                fi
                if [[ -n "$orig_shell" ]]; then
                    useradd_opts=("${useradd_opts[@]/-s \/usr\/sbin\/nologin/}")
                    useradd_opts+=(-s "$orig_shell")
                fi
            fi
            useradd "${useradd_opts[@]}" "$username" 2>&1 || {
                log_error "restore/metadata: Failed to create system user $username"
                return 1
            }
            # SFTP chroot: home dir must be owned by root:{user} 0750
            chown "root:${username}" "/home/${username}"
            chmod 0750 "/home/${username}"
            # Add to sftpusers group for SFTP access
            if getent group sftpusers &>/dev/null; then
                usermod -aG sftpusers "$username" 2>/dev/null || true
            fi
            log_info "restore/metadata: Created system user $username (root:${username} 0750, sftpusers)"
        fi

        # Create DB record with all required fields
        local random_pass
        random_pass=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)
        local hashed_pass
        hashed_pass=$(php -r "echo password_hash('${random_pass}', PASSWORD_BCRYPT);")

        _db_write "INSERT INTO users (username, name, email, password, home_directory, is_active, is_admin, email_verified_at, created_at, updated_at) VALUES ('$(mysql_escape "$username")', '$(mysql_escape "$username")', '$(mysql_escape "${user_email:-${username}@localhost}")', '${hashed_pass}', '/home/$(mysql_escape "$username")', 1, 0, NOW(), NOW(), NOW())"

        RESTORE_USER_ID=$(_db_query "SELECT id FROM users WHERE username='$(mysql_escape "$username")' LIMIT 1")

        if [[ -z "$RESTORE_USER_ID" ]]; then
            log_error "restore/metadata: Failed to create user $username in database"
            return 1
        fi
        RESTORE_USER_CREATED=1
        log_info "restore/metadata: Created user $username (id=$RESTORE_USER_ID)"
    fi

    # Store for phase 2
    export RESTORE_ACCOUNT_JSON="$account_json"
    export RESTORE_USER_ID
    export RESTORE_USER_CREATED

    log_info "restore/metadata: User ready (user_id=$RESTORE_USER_ID)"
}

# Phase 2: Create domains (called after files are restored)
# Creates domains directly in DB + sets up directory structure
restore_metadata_domains() {
    local username="$1"
    local account_json="${RESTORE_ACCOUNT_JSON:-}"

    [[ -z "$account_json" || ! -f "$account_json" ]] && return 0

    local domains_raw
    domains_raw=$(grep -oP '"domain"\s*:\s*"[^"]*"' "$account_json" | head -50)
    local uid="${RESTORE_USER_ID:-}"

    while IFS= read -r line; do
        local dname
        dname=$(echo "$line" | grep -oP '"domain"\s*:\s*"\K[^"]+')
        [[ -z "$dname" ]] && continue

        local existing_did
        existing_did=$(_db_query "SELECT id FROM domains WHERE domain='$(mysql_escape "$dname")' LIMIT 1")

        if [[ -n "$existing_did" ]]; then
            log_info "restore/metadata: Domain $dname exists, skipping"
        else
            log_info "restore/metadata: Creating domain $dname"

            # Create domain record in DB
            _db_write "INSERT INTO domains (user_id, domain, document_root, is_active, ssl_enabled, created_at, updated_at) VALUES ($uid, '$(mysql_escape "$dname")', '/home/$(mysql_escape "$username")/domains/$(mysql_escape "$dname")/public_html', 1, 1, NOW(), NOW())" || {
                log_warn "restore/metadata: Failed to create domain $dname in DB"
                continue
            }

            # Create directory structure
            local doc_root="/home/${username}/domains/${dname}/public_html"
            mkdir -p "$doc_root" 2>/dev/null || true
            mkdir -p "/home/${username}/domains/${dname}/logs" 2>/dev/null || true
            mkdir -p "/home/${username}/cache/nginx" 2>/dev/null || true
            chown -R "${username}:${username}" "/home/${username}/domains/${dname}" 2>/dev/null || true
            chown -R "${username}:${username}" "/home/${username}/cache" 2>/dev/null || true
        fi
    done <<< "$domains_raw"
}

# Lightweight JSON field extractor (no jq needed)
_json_val() {
    local file="$1" key="$2"
    grep -oP "${key}\s*:\s*\K[^,}]*" "$file" | head -1
}

_strip_json_str() { sed 's/^"//;s/"$//;s/^[[:space:]]*//;s/[[:space:]]*$//'; }
_strip_json_num() { sed 's/[^0-9]//g; s/^$/0/'; }
