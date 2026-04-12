#!/usr/bin/env bash
# Collector: PostgreSQL databases and roles
#
# Jabali naming conventions (see jabali/app/Filament/Jabali/Pages/Databases.php):
#   Database:  {username}_{dbname}   e.g. shuki_myapp
#   Role:      {username}_{db_user}  e.g. shuki_admin
#
# Jabali does NOT persist PostgreSQL credentials in the panel DB (unlike MySQL),
# so we rely on pg_dumpall --roles-only to capture password hashes.

collect_postgres() {
    local username="$1" staging_dir="$2"

    if ! command -v psql &>/dev/null; then
        log_info "postgres: psql not installed, skipping"
        return 0
    fi

    # Check postgres user exists (some systems use different names)
    if ! id postgres &>/dev/null; then
        log_info "postgres: postgres system user not found, skipping"
        return 0
    fi

    local pg_dir="${staging_dir}/postgres"

    # Find all databases with the username prefix
    # Includes databases owned by any {username}_* role, plus bare {username}
    local databases
    databases=$(sudo -u postgres psql -t -A -c \
        "SELECT datname FROM pg_database WHERE datname = '${username}' OR datname LIKE '${username}\\_%' ESCAPE '\\' ORDER BY datname" 2>/dev/null)

    # Find all roles with the username prefix
    local roles
    roles=$(sudo -u postgres psql -t -A -c \
        "SELECT rolname FROM pg_authid WHERE rolname = '${username}' OR rolname LIKE '${username}\\_%' ESCAPE '\\' ORDER BY rolname" 2>/dev/null)

    if [[ -z "$databases" ]] && [[ -z "$roles" ]]; then
        log_info "postgres: No PostgreSQL databases or roles for $username"
        return 0
    fi

    mkdir -p "$pg_dir"

    # Export roles with passwords (pg_dumpall --roles-only captures password hashes
    # as ALTER ROLE ... PASSWORD 'SCRAM-SHA-256$...' statements)
    if [[ -n "$roles" ]]; then
        local all_roles_sql
        all_roles_sql=$(sudo -u postgres pg_dumpall --roles-only 2>/dev/null)

        # Extract CREATE ROLE + ALTER ROLE + COMMENT statements for each matching role
        : > "${pg_dir}/roles.sql"
        while IFS= read -r role; do
            [[ -z "$role" ]] && continue
            # Skip system roles
            case "$role" in
                postgres|pg_*) continue ;;
            esac
            # grep all lines mentioning this role with a word boundary match
            # Role names appear quoted: "rolename"
            echo "$all_roles_sql" | grep -E "(CREATE|ALTER) ROLE \"?${role}\"?( |;|$)" >> "${pg_dir}/roles.sql"
            echo "" >> "${pg_dir}/roles.sql"
        done <<< "$roles"

        # Save role list for restore
        echo "$roles" | grep -v '^$' > "${pg_dir}/roles.txt"

        local role_count
        role_count=$(echo "$roles" | grep -cv '^$')
        log_info "postgres: Exported $role_count role(s) with password hashes"
    fi

    # Dump each database
    local db count=0
    while IFS= read -r db; do
        [[ -z "$db" ]] && continue
        # Skip system databases
        case "$db" in
            template0|template1|postgres) continue ;;
        esac
        log_info "postgres: Dumping database $db"
        if sudo -u postgres pg_dump -Fc "$db" > "${pg_dir}/${db}.dump" 2>/dev/null; then
            count=$((count + 1))

            # Capture database owner for restore
            local owner
            owner=$(sudo -u postgres psql -t -A -c \
                "SELECT rolname FROM pg_database JOIN pg_authid ON datdba = oid WHERE datname = '${db}'" 2>/dev/null)
            if [[ -n "$owner" ]]; then
                echo "$owner" > "${pg_dir}/${db}.owner"
            fi
        else
            log_warn "postgres: Failed to dump database $db"
            rm -f "${pg_dir}/${db}.dump"
        fi
    done <<< "$databases"

    if [[ "$count" -eq 0 ]] && [[ -z "$roles" ]]; then
        log_info "postgres: No data for $username"
    else
        log_info "postgres: Backed up $count database(s) for $username"
    fi
}
