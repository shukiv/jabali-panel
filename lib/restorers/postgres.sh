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

    if ! id postgres &>/dev/null; then
        log_error "restore/postgres: postgres system user not found"
        return 1
    fi

    # ── Restore roles ──
    # roles.sql contains CREATE ROLE + ALTER ROLE (with password hashes) for each
    # role matching the {username}_* prefix. The statements are idempotent-safe:
    # CREATE ROLE will fail if the role exists, but we use --force to drop first.
    local roles_file="${staging}/roles.sql"
    local roles_txt="${staging}/roles.txt"
    local -a restored_roles=()

    if [[ -f "$roles_txt" ]]; then
        while IFS= read -r role; do
            [[ -z "$role" ]] && continue

            local role_exists
            role_exists=$(sudo -u postgres psql -t -A -c \
                "SELECT 1 FROM pg_authid WHERE rolname = '${role}'" 2>/dev/null)

            if [[ -n "$role_exists" ]]; then
                if [[ "$force" -eq 1 ]]; then
                    # Drop role's owned objects first (required by PG)
                    sudo -u postgres psql -c "REASSIGN OWNED BY \"${role}\" TO postgres" 2>/dev/null || true
                    sudo -u postgres psql -c "DROP OWNED BY \"${role}\"" 2>/dev/null || true
                    sudo -u postgres psql -c "DROP ROLE IF EXISTS \"${role}\"" 2>/dev/null || true
                    log_info "restore/postgres: Dropped existing role $role (force)"
                else
                    log_info "restore/postgres: Role $role already exists, skipping"
                    restored_roles+=("$role")
                    continue
                fi
            fi
            restored_roles+=("$role")
        done < "$roles_txt"
    fi

    # Apply the full roles.sql (CREATE ROLE + ALTER ROLE with password hashes)
    if [[ -f "$roles_file" ]] && [[ -s "$roles_file" ]]; then
        # Filter out statements for roles that already exist and weren't force-dropped
        local apply_sql="${staging}/roles.apply.sql"
        cp "$roles_file" "$apply_sql"

        if sudo -u postgres psql -f "$apply_sql" 2>/dev/null; then
            log_info "restore/postgres: Applied role definitions (${#restored_roles[@]} roles, original passwords preserved)"
        else
            log_warn "restore/postgres: Some role statements failed (roles may already exist with different config)"
        fi
        rm -f "$apply_sql"
    fi

    # ── Restore databases ──
    local count=0
    for dump in "${staging}"/*.dump; do
        [[ ! -f "$dump" ]] && continue
        local db_name
        db_name=$(basename "$dump" .dump)

        # Read captured owner (fall back to username if not captured)
        local owner="$username"
        local owner_file="${staging}/${db_name}.owner"
        if [[ -f "$owner_file" ]]; then
            owner=$(cat "$owner_file")
        fi

        # Check if database exists
        local db_exists
        db_exists=$(sudo -u postgres psql -t -A -c \
            "SELECT 1 FROM pg_database WHERE datname = '${db_name}'" 2>/dev/null)

        local target_db="$db_name"
        if [[ -n "$db_exists" ]]; then
            if [[ "$force" -eq 1 ]]; then
                log_info "restore/postgres: Dropping and recreating $db_name"
                sudo -u postgres dropdb --if-exists "$db_name" 2>/dev/null || {
                    log_warn "restore/postgres: Failed to drop $db_name (connections may be active)"
                    continue
                }
            else
                target_db="${db_name}_restored"
                log_warn "restore/postgres: skipping overwrite of $db_name (exists) — restored as ${target_db} instead. Use --force to overwrite."
            fi
        fi

        # Create database with correct owner (fall back to postgres if owner doesn't exist)
        local owner_exists
        owner_exists=$(sudo -u postgres psql -t -A -c \
            "SELECT 1 FROM pg_authid WHERE rolname = '${owner}'" 2>/dev/null)

        if [[ -n "$owner_exists" ]]; then
            sudo -u postgres createdb -O "$owner" "$target_db" 2>/dev/null || {
                log_warn "restore/postgres: Failed to create database $target_db"
                continue
            }
        else
            sudo -u postgres createdb "$target_db" 2>/dev/null || {
                log_warn "restore/postgres: Failed to create database $target_db"
                continue
            }
            log_warn "restore/postgres: Owner $owner not found, created $target_db without owner"
        fi

        # Restore from custom-format dump
        # Use --no-owner because ownership is set at CREATE time; --no-privileges
        # because grants are in pg_dumpall output (roles.sql) not pg_dump output.
        if sudo -u postgres pg_restore -d "$target_db" --no-owner --no-privileges "$dump" 2>/dev/null; then
            count=$((count + 1))
            log_info "restore/postgres: Imported $db_name → $target_db (owner: $owner)"
        else
            # pg_restore returns non-zero on warnings too — verify data loaded
            local table_count
            table_count=$(sudo -u postgres psql -t -A -d "$target_db" -c \
                "SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')" 2>/dev/null)
            if [[ "${table_count:-0}" -gt 0 ]]; then
                count=$((count + 1))
                log_info "restore/postgres: Imported $db_name → $target_db (with warnings, $table_count tables)"
            else
                log_warn "restore/postgres: Failed to import $db_name"
            fi
        fi

        # Re-apply ownership to ensure all objects are owned by the correct role
        if [[ -n "$owner_exists" ]]; then
            sudo -u postgres psql -c \
                "ALTER DATABASE \"${target_db}\" OWNER TO \"${owner}\"" 2>/dev/null || true
            sudo -u postgres psql -d "$target_db" -c \
                "REASSIGN OWNED BY postgres TO \"${owner}\"" 2>/dev/null || true
        fi
    done

    log_info "restore/postgres: Restored $count database(s)"
}
