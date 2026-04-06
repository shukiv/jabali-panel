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
            # Check if MySQL user exists
            local user_exists
            user_exists=$(mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                -N -B -e "SELECT User FROM mysql.user WHERE User='$(mysql_escape "$mysql_user")' LIMIT 1" 2>/dev/null)
            if [[ -z "$user_exists" ]]; then
                local random_pass
                random_pass=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)
                mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                    -e "CREATE USER '$(mysql_escape "$mysql_user")'@'localhost' IDENTIFIED BY '$(mysql_escape "$random_pass")'" 2>/dev/null
                log_info "restore/mysql: Created MySQL user $mysql_user"

                # Record in mysql_credentials if user_id is known
                if [[ -n "$user_id" ]]; then
                    local existing_cred
                    existing_cred=$(_db_query "SELECT id FROM mysql_credentials WHERE user_id = $user_id LIMIT 1")
                    if [[ -z "$existing_cred" ]]; then
                        # Encrypt password using PHP (Laravel-compatible)
                        local encrypted_pass
                        encrypted_pass=$(php -r "
                            require '/var/www/jabali/vendor/autoload.php';
                            \$app = require_once '/var/www/jabali/bootstrap/app.php';
                            \$app->make('Illuminate\Contracts\Console\Kernel')->bootstrap();
                            echo encrypt('$random_pass');
                        " 2>/dev/null) || encrypted_pass=""
                        if [[ -n "$encrypted_pass" ]]; then
                            _db_write "INSERT INTO mysql_credentials (user_id, mysql_username, mysql_password_encrypted, created_at, updated_at) VALUES ($user_id, '$(mysql_escape "$mysql_user")', '$(mysql_escape "$encrypted_pass")', NOW(), NOW())"
                            log_info "restore/mysql: Recorded credentials for $mysql_user"
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

        # Check if database exists
        local db_exists
        db_exists=$(mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
            -N -B -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='$(mysql_escape "$db_name")'" 2>/dev/null)

        local target_db="$db_name"
        if [[ -n "$db_exists" ]]; then
            if [[ "$force" -eq 1 ]]; then
                log_info "restore/mysql: Dropping and recreating $db_name"
                mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                    -e "DROP DATABASE \`$(mysql_escape "$db_name")\`" 2>/dev/null
            else
                target_db="${db_name}_restored"
                log_info "restore/mysql: $db_name exists, restoring as $target_db"
            fi
        fi

        # Create database
        mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
            -e "CREATE DATABASE IF NOT EXISTS \`$(mysql_escape "$target_db")\`" 2>/dev/null

        # Import dump
        if gunzip -c "$dump" | mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
            "$target_db" 2>/dev/null; then
            count=$((count + 1))
            log_info "restore/mysql: Imported $db_name → $target_db"

            # Grant privileges to the MySQL user
            if [[ -f "$users_file" ]]; then
                local first_user
                first_user=$(head -1 "$users_file")
                if [[ -n "$first_user" ]]; then
                    mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                        -e "GRANT ALL PRIVILEGES ON \`$(mysql_escape "$target_db")\`.* TO '$(mysql_escape "$first_user")'@'localhost'" 2>/dev/null
                fi
            fi
        else
            log_warn "restore/mysql: Failed to import $db_name"
        fi
    done

    # Replay grants
    local grants_file="${staging}/grants.sql"
    if [[ -f "$grants_file" ]]; then
        while IFS= read -r grant_line; do
            [[ -z "$grant_line" || "$grant_line" == ";" ]] && continue
            mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                -e "$grant_line" 2>/dev/null || true
        done < "$grants_file"
        log_info "restore/mysql: Replayed grants"
    fi

    log_info "restore/mysql: Restored $count database(s)"
}
