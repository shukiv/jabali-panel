#!/usr/bin/env bash
# Collector: PostgreSQL databases and roles

collect_postgres() {
    local username="$1" staging_dir="$2"

    if ! command -v psql &>/dev/null; then
        log_info "postgres: psql not installed, skipping"
        return 0
    fi

    local pg_dir="${staging_dir}/postgres"

    # Check if user has any PG databases
    local databases
    databases=$(sudo -u postgres psql -t -A -c \
        "SELECT datname FROM pg_database JOIN pg_authid ON datdba = oid WHERE rolname = '${username}'" 2>/dev/null)

    if [[ -z "$databases" ]]; then
        log_info "postgres: No PostgreSQL databases for $username"
        return 0
    fi

    mkdir -p "$pg_dir"

    # Export role
    sudo -u postgres pg_dumpall --roles-only 2>/dev/null | grep -A2 "ROLE ${username}" > "${pg_dir}/role.sql"

    # Dump each database
    local db count=0
    while IFS= read -r db; do
        [[ -z "$db" ]] && continue
        log_info "postgres: Dumping database $db"
        if sudo -u postgres pg_dump -Fc "$db" > "${pg_dir}/${db}.dump" 2>/dev/null; then
            count=$((count + 1))
        else
            log_warn "postgres: Failed to dump database $db"
        fi
    done <<< "$databases"

    log_info "postgres: Backed up $count databases for $username"
}
