#!/usr/bin/env bash
# Restorer: PostgreSQL databases and roles

restore_postgres() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/postgres"

    [[ ! -d "$staging" ]] && return 0

    if ! command -v psql &>/dev/null; then
        log_error "restore/postgres: psql not installed, cannot restore"
        return 1
    fi

    # Restore role from role.sql (idempotent — skip if exists)
    local role_file="${staging}/role.sql"
    if [[ -f "$role_file" ]]; then
        local role_exists
        role_exists=$(sudo -u postgres psql -t -A -c \
            "SELECT 1 FROM pg_authid WHERE rolname = '${username}'" 2>/dev/null)
        if [[ -z "$role_exists" ]]; then
            sudo -u postgres psql -f "$role_file" 2>/dev/null || true
            log_info "restore/postgres: Created role $username"
        else
            log_info "restore/postgres: Role $username already exists"
        fi
    fi

    local count=0
    for dump in "${staging}"/*.dump; do
        [[ ! -f "$dump" ]] && continue
        local db_name
        db_name=$(basename "$dump" .dump)

        # Check if database exists
        local db_exists
        db_exists=$(sudo -u postgres psql -t -A -c \
            "SELECT 1 FROM pg_database WHERE datname = '${db_name}'" 2>/dev/null)

        local target_db="$db_name"
        if [[ -n "$db_exists" ]]; then
            if [[ "$force" -eq 1 ]]; then
                log_info "restore/postgres: Dropping and recreating $db_name"
                sudo -u postgres dropdb "$db_name" 2>/dev/null || true
            else
                target_db="${db_name}_restored"
                log_info "restore/postgres: $db_name exists, restoring as $target_db"
            fi
        fi

        # Create database owned by the user
        sudo -u postgres createdb -O "$username" "$target_db" 2>/dev/null || {
            # If role doesn't exist for ownership, create without owner
            sudo -u postgres createdb "$target_db" 2>/dev/null || {
                log_warn "restore/postgres: Failed to create database $target_db"
                continue
            }
        }

        # Restore from custom-format dump
        if sudo -u postgres pg_restore -d "$target_db" --no-owner --no-privileges "$dump" 2>/dev/null; then
            count=$((count + 1))
            log_info "restore/postgres: Imported $db_name → $target_db"

            # Grant ownership
            sudo -u postgres psql -c \
                "ALTER DATABASE \"${target_db}\" OWNER TO \"${username}\"" 2>/dev/null || true
        else
            # pg_restore returns non-zero on warnings too — check if data was loaded
            local table_count
            table_count=$(sudo -u postgres psql -t -A -d "$target_db" -c \
                "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'" 2>/dev/null)
            if [[ "${table_count:-0}" -gt 0 ]]; then
                count=$((count + 1))
                log_info "restore/postgres: Imported $db_name → $target_db (with warnings)"
                sudo -u postgres psql -c \
                    "ALTER DATABASE \"${target_db}\" OWNER TO \"${username}\"" 2>/dev/null || true
            else
                log_warn "restore/postgres: Failed to import $db_name"
            fi
        fi
    done

    log_info "restore/postgres: Restored $count database(s)"
}
