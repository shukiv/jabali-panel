#!/usr/bin/env bash
# Collector: MySQL databases and grants

collect_mysql() {
    local user_id="$1" staging_dir="$2" username="$3"
    local mysql_user

    mysql_user="$(discover_mysql_credentials "$user_id")"

    local mysql_dir="${staging_dir}/mysql"
    mkdir -p "$mysql_dir"

    # Build list of database name prefixes to search
    # 1. From mysql_credentials table (e.g. shuki_admin)
    # 2. From username itself (e.g. shuki_)
    local -a prefixes=()
    if [[ -n "$mysql_user" ]]; then
        prefixes+=("${mysql_user}_%")
        prefixes+=("${mysql_user}")
    fi
    if [[ -n "$username" ]]; then
        prefixes+=("${username}_%")
        prefixes+=("${username}")
    fi

    # Build LIKE conditions and deduplicate
    local like_clauses=""
    local -A seen=()
    for prefix in "${prefixes[@]}"; do
        [[ -n "${seen[$prefix]:-}" ]] && continue
        seen[$prefix]=1
        [[ -n "$like_clauses" ]] && like_clauses+=" OR "
        like_clauses+="SCHEMA_NAME LIKE '$(mysql_escape "$prefix")%'"
    done

    if [[ -z "$like_clauses" ]]; then
        log_info "mysql: No MySQL user or username to search databases, skipping"
        return 0
    fi

    # List databases owned by this user
    local databases
    databases=$(mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
        -N -B -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE $like_clauses" 2>/dev/null)

    # Dump each database
    local db count=0
    while IFS= read -r db; do
        [[ -z "$db" ]] && continue
        # Skip system databases
        case "$db" in
            information_schema|performance_schema|mysql|sys|jabali) continue ;;
        esac
        log_info "mysql: Dumping database $db"
        if mysqldump -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
            --single-transaction --routines --triggers --events \
            "$db" 2>/dev/null | gzip > "${mysql_dir}/${db}.sql.gz"; then
            count=$((count + 1))
        else
            log_warn "mysql: Failed to dump database $db"
        fi
    done <<< "$databases"

    # Export grants for MySQL users matching this account
    local -a mysql_users=()
    [[ -n "$mysql_user" ]] && mysql_users+=("$mysql_user")
    # Also find any MySQL users prefixed with the username
    local extra_users
    extra_users=$(mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
        -N -B -e "SELECT DISTINCT User FROM mysql.user WHERE User LIKE '$(mysql_escape "$username")%'" 2>/dev/null)
    while IFS= read -r u; do
        [[ -z "$u" ]] && continue
        mysql_users+=("$u")
    done <<< "$extra_users"

    if [[ ${#mysql_users[@]} -gt 0 ]]; then
        : > "${mysql_dir}/grants.sql"
        local -A grant_seen=()
        for mu in "${mysql_users[@]}"; do
            [[ -n "${grant_seen[$mu]:-}" ]] && continue
            grant_seen[$mu]=1
            local hosts
            hosts=$(mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                -N -B -e "SELECT DISTINCT Host FROM mysql.user WHERE User='$(mysql_escape "$mu")'" 2>/dev/null)
            while IFS= read -r host; do
                [[ -z "$host" ]] && continue
                mysql -h "$CFG_DB_HOST" -u "$CFG_DB_USER" -p"$CFG_DB_PASS" \
                    -N -B -e "SHOW GRANTS FOR '$(mysql_escape "$mu")'@'$(mysql_escape "$host")'" 2>/dev/null \
                    >> "${mysql_dir}/grants.sql"
                echo ";" >> "${mysql_dir}/grants.sql"
            done <<< "$hosts"
        done
        log_info "mysql: Exported grants for ${mysql_users[*]}"

        # Save MySQL user list for restore
        printf '%s\n' "${mysql_users[@]}" | sort -u > "${mysql_dir}/users.txt"
    fi

    if [[ "$count" -eq 0 ]]; then
        log_info "mysql: No databases found for $username"
    else
        log_info "mysql: Backed up $count database(s) for $username"
    fi
}
