#!/usr/bin/env bash
# Restorer: MySQL databases and grants

restore_mysql() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/mysql"

    [[ ! -d "$staging" ]] && return 0

    # Recreate MySQL users from users.txt (saved by collector)
    local users_file="${staging}/users.txt"
    local create_users_file="${staging}/create_users.sql"
    if [[ -f "$users_file" ]]; then
        while IFS= read -r mysql_user; do
            [[ -z "$mysql_user" ]] && continue
            local user_exists
            user_exists=$(_mysql_root_query "SELECT User FROM mysql.user WHERE User='$(mysql_escape "$mysql_user")' LIMIT 1")

            if [[ -n "$user_exists" ]] && [[ "$force" -eq 0 ]]; then
                log_info "restore/mysql: MySQL user $mysql_user already exists"
                # Sync password: if a credential exists in jabali, set MySQL to match it
                _mysql_sync_credential_password "$mysql_user" "$user_id"
                continue
            fi

            # Drop existing user if force mode
            if [[ -n "$user_exists" ]] && [[ "$force" -eq 1 ]]; then
                _mysql_root_write "DROP USER '$(mysql_escape "$mysql_user")'@'localhost'" 2>/dev/null || true
            fi

            # Try to recreate from CREATE USER statement (preserves original auth hash)
            local created_from_backup=0
            if [[ -f "$create_users_file" ]]; then
                local create_stmt
                create_stmt=$(grep -i "CREATE USER.*'$(mysql_escape "$mysql_user")'" "$create_users_file" | head -1)
                if [[ -n "$create_stmt" ]]; then
                    if _mysql_root_write "$create_stmt" 2>/dev/null; then
                        created_from_backup=1
                        log_info "restore/mysql: Recreated MySQL user $mysql_user from backup (original auth preserved)"
                        # Sync the MySQL password to match the panel credential
                        _mysql_sync_credential_password "$mysql_user" "$user_id"
                    fi
                fi
            fi

            # Fallback: create with new password and record in credentials
            if [[ "$created_from_backup" -eq 0 ]]; then
                local random_pass
                random_pass=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)
                if _mysql_root_write "CREATE USER '$(mysql_escape "$mysql_user")'@'localhost' IDENTIFIED BY '$(mysql_escape "$random_pass")'"; then
                    log_info "restore/mysql: Created MySQL user $mysql_user (new password)"
                else
                    log_warn "restore/mysql: Failed to create MySQL user $mysql_user"
                    continue
                fi

                # Record in mysql_credentials
                if [[ -n "$user_id" ]]; then
                    local existing_cred
                    existing_cred=$(_db_query "SELECT id FROM mysql_credentials WHERE user_id = $user_id AND mysql_username = '$(mysql_escape "$mysql_user")' LIMIT 1")
                    if [[ -z "$existing_cred" ]]; then
                        local encrypted_pass=""
                        if [[ -n "$CFG_APP_KEY" ]]; then
                            encrypted_pass=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-encrypt.php" "$random_pass" 2>/dev/null) || encrypted_pass=""
                        fi
                        if [[ -n "$encrypted_pass" ]]; then
                            _db_write "INSERT INTO mysql_credentials (user_id, mysql_username, mysql_password_encrypted, created_at, updated_at) VALUES ($user_id, '$(mysql_escape "$mysql_user")', '$(mysql_escape "$encrypted_pass")', NOW(), NOW())"
                            log_info "restore/mysql: Recorded credentials for $mysql_user"
                        fi
                    else
                        # Credential exists — update MySQL password to match it
                        _mysql_sync_credential_password "$mysql_user" "$user_id"
                    fi
                fi
            fi
        done < "$users_file"
    fi

    local count=0
    for dump in "${staging}"/*.sql.gz; do
        [[ ! -f "$dump" ]] && continue
        local db_name
        db_name=$(basename "$dump" .sql.gz)

        # Check if database exists (needs root to see all databases)
        local db_exists
        db_exists=$(_mysql_root_query "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='$(mysql_escape "$db_name")'")

        local target_db="$db_name"
        if [[ -n "$db_exists" ]]; then
            if [[ "$force" -eq 1 ]]; then
                log_info "restore/mysql: Dropping and recreating $db_name"
                _mysql_root_write "DROP DATABASE \`$(mysql_escape "$db_name")\`" || {
                    log_warn "restore/mysql: Failed to drop $db_name"
                    continue
                }
            else
                target_db="${db_name}_restored"
                log_warn "restore/mysql: skipping overwrite of $db_name (exists) — restored as ${target_db} instead. Use --force to overwrite."
            fi
        fi

        # Create database (needs root for CREATE DATABASE privilege)
        if ! _mysql_root_write "CREATE DATABASE IF NOT EXISTS \`$(mysql_escape "$target_db")\`"; then
            log_warn "restore/mysql: Failed to create database $target_db"
            continue
        fi

        # Import dump (root can write to any database)
        if gunzip -c "$dump" | _mysql_root "$target_db" 2>/dev/null; then
            count=$((count + 1))
            log_info "restore/mysql: Imported $db_name → $target_db"

            # Grant privileges to the MySQL user
            if [[ -f "$users_file" ]]; then
                local first_user
                first_user=$(head -1 "$users_file")
                if [[ -n "$first_user" ]]; then
                    _mysql_root_write "GRANT ALL PRIVILEGES ON \`$(mysql_escape "$target_db")\`.* TO '$(mysql_escape "$first_user")'@'localhost'" \
                        || log_warn "restore/mysql: Failed to grant privileges on $target_db to $first_user"
                fi
            fi
        else
            log_warn "restore/mysql: Failed to import $db_name"
        fi
    done

    # Replay grants (needs root for GRANT statements)
    local grants_file="${staging}/grants.sql"
    if [[ -f "$grants_file" ]] && [[ -s "$grants_file" ]]; then
        while IFS= read -r grant_line; do
            [[ -z "$grant_line" || "$grant_line" == ";" ]] && continue
            _mysql_root_write "$grant_line" 2>/dev/null || true
        done < "$grants_file"
        log_info "restore/mysql: Replayed grants"
    fi

    log_info "restore/mysql: Restored $count database(s)"
}

# Sync MySQL user password to match the encrypted credential stored in jabali panel.
# Decrypts the password from mysql_credentials and runs ALTER USER to set it.
_mysql_sync_credential_password() {
    local mysql_user="$1" user_id="$2"
    [[ -z "$user_id" || -z "$CFG_APP_KEY" ]] && return 0

    local encrypted_pass
    encrypted_pass=$(_db_query "SELECT mysql_password_encrypted FROM mysql_credentials WHERE user_id = $user_id AND mysql_username = '$(mysql_escape "$mysql_user")' LIMIT 1")
    [[ -z "$encrypted_pass" ]] && return 0

    local plain_pass
    plain_pass=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-decrypt.php" "$encrypted_pass" 2>/dev/null) || return 0
    [[ -z "$plain_pass" ]] && return 0

    if _mysql_root_write "ALTER USER '$(mysql_escape "$mysql_user")'@'localhost' IDENTIFIED BY '$(mysql_escape "$plain_pass")'"; then
        log_info "restore/mysql: Synced MySQL password for $mysql_user with panel credential"
    else
        log_warn "restore/mysql: Failed to sync password for $mysql_user"
    fi
}
