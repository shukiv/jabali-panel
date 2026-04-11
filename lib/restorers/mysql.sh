#!/usr/bin/env bash
# Restorer: MySQL databases and grants

restore_mysql() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/mysql"

    [[ ! -d "$staging" ]] && return 0

    # Recreate MySQL users from users.txt (saved by collector)
    local users_file="${staging}/users.txt"
    if [[ -f "$users_file" ]]; then
        while IFS= read -r mysql_user; do
            [[ -z "$mysql_user" ]] && continue
            # Check if MySQL user exists (needs root to query mysql.user)
            local user_exists
            user_exists=$(_mysql_root_query "SELECT User FROM mysql.user WHERE User='$(mysql_escape "$mysql_user")' LIMIT 1")
            if [[ -z "$user_exists" ]]; then
                local random_pass
                random_pass=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)
                if _mysql_root_write "CREATE USER '$(mysql_escape "$mysql_user")'@'localhost' IDENTIFIED BY '$(mysql_escape "$random_pass")'"; then
                    log_info "restore/mysql: Created MySQL user $mysql_user"
                else
                    log_warn "restore/mysql: Failed to create MySQL user $mysql_user"
                    continue
                fi

                # Record in mysql_credentials if user_id is known
                if [[ -n "$user_id" ]]; then
                    local existing_cred
                    existing_cred=$(_db_query "SELECT id FROM mysql_credentials WHERE user_id = $user_id AND mysql_username = '$(mysql_escape "$mysql_user")' LIMIT 1")
                    if [[ -z "$existing_cred" ]]; then
                        # Encrypt password using PHP helper
                        local encrypted_pass=""
                        if [[ -n "$CFG_APP_KEY" ]]; then
                            encrypted_pass=$(JABALI_APP_KEY="$CFG_APP_KEY" php "$LIB_DIR/jabali-encrypt.php" "$random_pass" 2>/dev/null) || encrypted_pass=""
                        fi
                        if [[ -n "$encrypted_pass" ]]; then
                            _db_write "INSERT INTO mysql_credentials (user_id, mysql_username, mysql_password_encrypted, created_at, updated_at) VALUES ($user_id, '$(mysql_escape "$mysql_user")', '$(mysql_escape "$encrypted_pass")', NOW(), NOW())" \
                                && log_info "restore/mysql: Recorded credentials for $mysql_user" \
                                || log_warn "restore/mysql: Failed to record credentials for $mysql_user"
                        fi
                    fi
                fi
            else
                log_info "restore/mysql: MySQL user $mysql_user already exists"
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
