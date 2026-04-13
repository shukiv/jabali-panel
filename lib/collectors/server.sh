#!/usr/bin/env bash
# Collector: Full server backup for disaster recovery
# Collects all server-level config, databases, and services per backup-server-spec.md

collect_server() {
    local staging_dir="$1"
    local server_dir="${staging_dir}/server"
    mkdir -p "$server_dir"

    log_info "server: Starting full server collection"

    _collect_server_databases "$server_dir"
    _collect_server_panel "$server_dir"
    _collect_server_jabali_config "$server_dir"
    _collect_server_nginx "$server_dir"
    _collect_server_php "$server_dir"
    _collect_server_stalwart "$server_dir"
    _collect_server_redis "$server_dir"
    _collect_server_mysql_config "$server_dir"
    _collect_server_ssl "$server_dir"
    _collect_server_letsencrypt "$server_dir"
    _collect_server_systemd "$server_dir"
    _collect_server_packages "$server_dir"
    _collect_server_bulwark "$server_dir"
    _generate_server_manifest "$server_dir"

    log_info "server: Full server collection complete"
}

# ── 1. Panel + PowerDNS databases ──

_collect_server_databases() {
    local server_dir="$1"
    local db_dir="${server_dir}/databases"
    mkdir -p "$db_dir"

    # Ephemeral tables to skip in jabali DB
    local skip_tables=(
        sessions cache cache_locks jobs job_batches failed_jobs
        impersonation_tokens personal_access_tokens server_processes
        password_reset_tokens
    )
    local ignore_args=()
    for t in "${skip_tables[@]}"; do
        ignore_args+=(--ignore-table="jabali.${t}")
    done

    # jabali database
    if mysqldump -u root --single-transaction --quick --routines --triggers \
        "${ignore_args[@]}" jabali 2>/dev/null | gzip > "${db_dir}/jabali.sql.gz"; then
        log_info "server: Dumped jabali database"
    else
        log_warn "server: Failed to dump jabali database"
    fi

    # powerdns database
    if mysqldump -u root --single-transaction --quick powerdns 2>/dev/null \
        | gzip > "${db_dir}/powerdns.sql.gz"; then
        log_info "server: Dumped powerdns database"
    else
        log_warn "server: Failed to dump powerdns database"
    fi

    # DNSSEC state
    if command -v pdnsutil &>/dev/null; then
        local zones
        zones=$(pdnsutil list-all-zones 2>/dev/null)
        if [[ -n "$zones" ]]; then
            while IFS= read -r zone; do
                [[ -z "$zone" ]] && continue
                pdnsutil show-zone "$zone" >> "${db_dir}/dnssec-state.txt" 2>/dev/null
                echo "---" >> "${db_dir}/dnssec-state.txt"
            done <<< "$zones"
            log_info "server: Exported DNSSEC state for $(echo "$zones" | wc -l) zone(s)"
        fi
    fi
}

# ── 3-4. Panel app + .env ──

_collect_server_panel() {
    local server_dir="$1"
    local panel_dir="${server_dir}/panel"
    mkdir -p "$panel_dir"

    local jabali_path="${CFG_JABALI_PATH:-/var/www/jabali}"

    # .env file (contains APP_KEY and all secrets)
    if [[ -f "${jabali_path}/.env" ]]; then
        cp "${jabali_path}/.env" "${panel_dir}/env"
        chmod 600 "${panel_dir}/env"
        log_info "server: Backed up panel .env"
    fi

    # Storage (uploaded branding, logos)
    if [[ -d "${jabali_path}/storage/app" ]]; then
        mkdir -p "${panel_dir}/storage"
        cp -a "${jabali_path}/storage/app" "${panel_dir}/storage/app"
        log_info "server: Backed up panel storage/app"
    fi

    # Git info for clone-based restore, plus dirty-tree capture so uncommitted
    # work isn't silently lost on a git-clone-style restore.
    if [[ -d "${jabali_path}/.git" ]]; then
        local remote commit
        remote=$(git -C "$jabali_path" remote get-url origin 2>/dev/null || echo "unknown")
        commit=$(git -C "$jabali_path" rev-parse HEAD 2>/dev/null || echo "unknown")
        printf '{"remote":"%s","commit":"%s"}\n' "$remote" "$commit" > "${panel_dir}/git-info.json"

        git -C "$jabali_path" status --porcelain > "${panel_dir}/git-dirty.txt" 2>/dev/null || true
        git -C "$jabali_path" diff > "${panel_dir}/git-dirty.patch" 2>/dev/null || true
        if [[ -s "${panel_dir}/git-dirty.txt" ]]; then
            log_warn "server: panel tree has uncommitted changes — captured as git-dirty.patch"
        fi
        echo "git" > "${panel_dir}/restore-mode.txt"
    else
        # No .git — snapshot the source tree so restore can rehydrate without
        # a remote clone. Excludes follow spec §3 "What NOT to back up".
        echo "tarball" > "${panel_dir}/restore-mode.txt"
        mkdir -p "${panel_dir}/source"
        tar -C "$jabali_path" -cf - \
            --exclude='./vendor' \
            --exclude='./node_modules' \
            --exclude='./storage/framework/cache' \
            --exclude='./storage/framework/views' \
            --exclude='./bootstrap/cache' \
            --exclude='./.git' \
            . 2>/dev/null | tar -C "${panel_dir}/source" -xf - 2>/dev/null
        log_info "server: Captured panel source tree (no .git — tarball restore)"
    fi

    # Composer files for dependency restore
    for f in composer.json composer.lock package.json package-lock.json; do
        [[ -f "${jabali_path}/${f}" ]] && cp "${jabali_path}/${f}" "${panel_dir}/${f}"
    done
}

# ── 5. /etc/jabali/ ──

_collect_server_jabali_config() {
    local server_dir="$1"
    local config_dir="${server_dir}/config/jabali"

    if [[ -d "/etc/jabali" ]]; then
        mkdir -p "$config_dir"
        cp -a /etc/jabali/. "$config_dir/"
        log_info "server: Backed up /etc/jabali/"
    fi
}

# ── 6. Nginx config ──

_collect_server_nginx() {
    local server_dir="$1"
    local nginx_dir="${server_dir}/config/nginx"
    mkdir -p "$nginx_dir"

    # Main config
    [[ -f /etc/nginx/nginx.conf ]] && cp /etc/nginx/nginx.conf "$nginx_dir/"

    # Jabali managed includes and cache zones
    if [[ -d /etc/nginx/jabali ]]; then
        cp -a /etc/nginx/jabali "$nginx_dir/jabali"
    fi

    # All vhost configs
    if [[ -d /etc/nginx/sites-available ]]; then
        mkdir -p "$nginx_dir/sites-available"
        cp /etc/nginx/sites-available/*.conf "$nginx_dir/sites-available/" 2>/dev/null
    fi

    # Record enabled sites (symlink state)
    if [[ -d /etc/nginx/sites-enabled ]]; then
        ls -1 /etc/nginx/sites-enabled/ > "$nginx_dir/sites-enabled.txt" 2>/dev/null
    fi

    log_info "server: Backed up nginx config"
}

# ── 7. PHP-FPM config ──

_collect_server_php() {
    local server_dir="$1"
    local php_dir="${server_dir}/config/php"
    mkdir -p "$php_dir"

    for ver_dir in /etc/php/*/; do
        [[ ! -d "$ver_dir" ]] && continue
        local ver
        ver=$(basename "$ver_dir")
        local fpm_dir="${ver_dir}fpm"
        [[ ! -d "$fpm_dir" ]] && continue

        local dest="${php_dir}/${ver}"
        mkdir -p "$dest"

        # Global FPM config
        [[ -f "${fpm_dir}/php-fpm.conf" ]] && cp "${fpm_dir}/php-fpm.conf" "$dest/"
        [[ -f "${fpm_dir}/php.ini" ]] && cp "${fpm_dir}/php.ini" "$dest/"

        # Server-level pools (www, admin)
        for pool in www.conf admin.conf; do
            [[ -f "${fpm_dir}/pool.d/${pool}" ]] && cp "${fpm_dir}/pool.d/${pool}" "$dest/"
        done
    done

    log_info "server: Backed up PHP-FPM config"
}

# ── 10. Stalwart mail config ──

_collect_server_stalwart() {
    local server_dir="$1"

    # 10a. /etc/stalwart-mail — config (always safe to hot-copy)
    if [[ -d /etc/stalwart-mail ]]; then
        local dest="${server_dir}/config/stalwart"
        mkdir -p "$dest"
        cp -a /etc/stalwart-mail/. "$dest/"
        log_info "server: Backed up Stalwart config"
    fi

    # 10b. /var/lib/stalwart-mail — RocksDB (DKIM private keys + mail data)
    # Losing this means DKIM signatures break for every domain until keys are
    # regenerated and DNS is re-published. Stop the service before rsync to
    # avoid hot-copying an inconsistent RocksDB state.
    if [[ "${CFG_SERVER_BACKUP_STALWART_DATA:-true}" != "true" ]]; then
        log_info "server: Skipping Stalwart data (--skip-stalwart-data)"
        return 0
    fi

    if [[ ! -d /var/lib/stalwart-mail ]]; then
        return 0
    fi

    local data_dest="${server_dir}/data/stalwart"
    mkdir -p "$data_dest"

    local was_active=0
    if systemctl is-active --quiet stalwart-mail 2>/dev/null; then
        was_active=1
        log_info "server: Stopping stalwart-mail briefly to snapshot RocksDB"
        systemctl stop stalwart-mail 2>/dev/null || log_warn "server: stop stalwart-mail returned non-zero"
    fi

    if command -v rsync &>/dev/null; then
        rsync -a --numeric-ids /var/lib/stalwart-mail/ "$data_dest/" 2>/dev/null \
            && log_info "server: Backed up Stalwart data (DKIM + mail)" \
            || log_warn "server: rsync of /var/lib/stalwart-mail failed"
    else
        cp -a /var/lib/stalwart-mail/. "$data_dest/" 2>/dev/null \
            && log_info "server: Backed up Stalwart data (cp fallback)" \
            || log_warn "server: cp of /var/lib/stalwart-mail failed"
    fi

    if [[ "$was_active" -eq 1 ]]; then
        systemctl start stalwart-mail 2>/dev/null || log_error "server: stalwart-mail failed to start"
        local i
        for i in {1..30}; do
            systemctl is-active --quiet stalwart-mail && break
            sleep 0.5
        done
        if ! systemctl is-active --quiet stalwart-mail; then
            log_error "server: CRITICAL — stalwart-mail did not come back active after data capture"
            return 2
        fi
        log_info "server: stalwart-mail restarted and active"
    fi
}

# ── 11. Redis config ──

_collect_server_redis() {
    local server_dir="$1"

    if [[ -f /etc/redis/redis.conf ]]; then
        local dest="${server_dir}/config/redis"
        mkdir -p "$dest"
        cp /etc/redis/redis.conf "$dest/"
        chmod 600 "$dest/redis.conf"
        log_info "server: Backed up Redis config (contains ACL passwords)"
    fi
}

# ── 12. MariaDB config ──

_collect_server_mysql_config() {
    local server_dir="$1"
    local dest="${server_dir}/config/mysql"
    mkdir -p "$dest"

    [[ -d /etc/mysql/mariadb.conf.d ]] && cp -a /etc/mysql/mariadb.conf.d "$dest/"
    if [[ -f /etc/mysql/debian.cnf ]]; then
        cp /etc/mysql/debian.cnf "$dest/"
        chmod 600 "$dest/debian.cnf"
    fi

    log_info "server: Backed up MariaDB config"
}

# ── 9. Panel SSL cert ──

_collect_server_ssl() {
    local server_dir="$1"

    if [[ -d /etc/ssl/jabali ]]; then
        local dest="${server_dir}/ssl/jabali"
        mkdir -p "$dest"
        cp -a /etc/ssl/jabali/. "$dest/"
        chmod 600 "$dest"/*.key 2>/dev/null || true
        log_info "server: Backed up panel SSL cert"
    fi
}

# ── 17. Let's Encrypt state ──

_collect_server_letsencrypt() {
    local server_dir="$1"

    if [[ -d /etc/letsencrypt ]]; then
        local dest="${server_dir}/letsencrypt"
        mkdir -p "$dest"
        # Copy with symlinks resolved in live/ (actual PEM files)
        cp -aL /etc/letsencrypt/. "$dest/" 2>/dev/null || cp -a /etc/letsencrypt/. "$dest/"
        log_info "server: Backed up Let's Encrypt state"
    fi
}

# ── 13. Systemd units ──

_collect_server_systemd() {
    local server_dir="$1"
    local dest="${server_dir}/systemd"
    mkdir -p "$dest"

    local count=0
    for unit in /etc/systemd/system/jabali-*.service \
                /etc/systemd/system/jabali-*.timer \
                /etc/systemd/system/jabali.slice \
                /etc/systemd/system/stalwart-mail.service \
                /etc/systemd/system/bulwark.service; do
        if [[ -f "$unit" ]]; then
            cp "$unit" "$dest/"
            count=$((count + 1))
        fi
    done

    # Nginx override dir
    if [[ -d /etc/systemd/system/nginx.service.d ]]; then
        cp -a /etc/systemd/system/nginx.service.d "$dest/"
        count=$((count + 1))
    fi

    log_info "server: Backed up $count systemd unit(s)"
}

# ── 20. Package manifest ──

_collect_server_packages() {
    local server_dir="$1"
    local dest="${server_dir}/packages"
    mkdir -p "$dest"

    dpkg --get-selections > "${dest}/dpkg-selections.txt" 2>/dev/null
    apt-mark showmanual > "${dest}/manual-packages.txt" 2>/dev/null
    php -v > "${dest}/php-version.txt" 2>/dev/null || true
    nginx -v 2> "${dest}/nginx-version.txt" || true
    mysql --version > "${dest}/mysql-version.txt" 2>/dev/null || true
    cat /etc/os-release > "${dest}/os-release.txt" 2>/dev/null || true

    log_info "server: Exported package manifest"
}

# ── Server manifest ──

# ── 18. Bulwark webmail (metadata only; reinstall on restore) ──
# Spec §18: preferred restore is reinstall via install.sh, not file copy. We
# record just enough metadata (version, port, upstream SHA) to let the operator
# recreate the install deterministically.
_collect_server_bulwark() {
    local server_dir="$1"
    local meta_dir="${server_dir}/metadata"
    mkdir -p "$meta_dir"

    if [[ ! -d /opt/bulwark ]]; then
        return 0
    fi

    local pkg_version="" upstream_sha="" port=""

    if [[ -f /opt/bulwark/package.json ]] && command -v jq &>/dev/null; then
        pkg_version=$(jq -r '.version // ""' /opt/bulwark/package.json 2>/dev/null)
    fi

    if [[ -d /opt/bulwark/.git ]]; then
        upstream_sha=$(git -C /opt/bulwark rev-parse HEAD 2>/dev/null || echo "")
    fi

    if [[ -f /etc/systemd/system/bulwark.service ]]; then
        port=$(grep -oP 'PORT=\K\d+' /etc/systemd/system/bulwark.service 2>/dev/null || echo "")
    fi

    cat > "${meta_dir}/bulwark.json" <<EOB
{
  "installed": true,
  "package_version": "${pkg_version}",
  "upstream_sha": "${upstream_sha}",
  "port": "${port}",
  "restore_hint": "reinstall via: sudo /opt/jabali-backup/install.sh --reinstall-bulwark"
}
EOB
    log_info "server: Recorded Bulwark metadata (reinstall on restore)"
}

_generate_server_manifest() {
    local server_dir="$1"

    local user_count domain_count db_count
    user_count=$(_db_query "SELECT COUNT(*) FROM users WHERE is_active = 1" 2>/dev/null || echo "0")
    domain_count=$(_db_query "SELECT COUNT(*) FROM domains" 2>/dev/null || echo "0")
    db_count=$(_mysql_root_query "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME NOT IN ('information_schema','performance_schema','mysql','sys','jabali','powerdns')" 2>/dev/null || echo "0")

    local users_list
    users_list=$(_db_query "SELECT username FROM users WHERE is_active = 1 ORDER BY username" 2>/dev/null | paste -sd',' || echo "")
    local users_json
    users_json=$(echo "$users_list" | tr ',' '\n' | sed '/^$/d' | sed 's/.*/"&"/' | paste -sd',' | sed 's/^/[/;s/$/]/')
    [[ "$users_json" == "[]" ]] && users_json="[]"

    local jabali_path="${CFG_JABALI_PATH:-/var/www/jabali}"
    local jabali_ver=""
    [[ -f "${jabali_path}/VERSION" ]] && jabali_ver=$(cat "${jabali_path}/VERSION" 2>/dev/null)
    [[ -z "$jabali_ver" ]] && jabali_ver=$(grep -oP "'version'\s*=>\s*'\K[^']+" "${jabali_path}/config/app.php" 2>/dev/null || echo "unknown")

    local php_versions
    php_versions=$(ls -1 /etc/php/ 2>/dev/null | sed 's/.*/"&"/' | paste -sd',' | sed 's/^/[/;s/$/]/')
    [[ -z "$php_versions" || "$php_versions" == "[]" ]] && php_versions='["unknown"]'

    local agent_addons='[]'
    if [[ -d /etc/jabali/agent.d ]]; then
        agent_addons=$(ls -1 /etc/jabali/agent.d/*.php 2>/dev/null | sed 's/.*\///;s/\.php$//' | sed 's/.*/"&"/' | paste -sd',' | sed 's/^/[/;s/$/]/')
        [[ -z "$agent_addons" ]] && agent_addons='[]'
    fi

    cat > "${server_dir}/server-manifest.json" <<EOMANIFEST
{
  "version": "${JABALI_BACKUP_VERSION:-dev}",
  "type": "server",
  "created_at": "$(date -Iseconds)",
  "hostname": "$(hostname)",
  "ip_address": "$(hostname -I 2>/dev/null | awk '{print $1}')",
  "os": "$(grep -oP 'PRETTY_NAME="\K[^"]+' /etc/os-release 2>/dev/null || echo 'unknown')",
  "jabali_version": "${jabali_ver}",
  "php_versions": ${php_versions},
  "nginx_version": "$(nginx -v 2>&1 | grep -oP 'nginx/\K\S+' || echo 'unknown')",
  "mariadb_version": "$(mysql --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo 'unknown')",
  "stalwart_version": "$(stalwart-mail --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' || echo 'unknown')",
  "powerdns_version": "$(pdns_server --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' || echo 'unknown')",
  "users": ${users_json},
  "user_count": ${user_count:-0},
  "domain_count": ${domain_count:-0},
  "database_count": ${db_count:-0},
  "databases_backed_up": ["jabali", "powerdns"],
  "components": {
    "panel_app": true,
    "env_file": $(if [[ -f "${jabali_path}/.env" ]]; then echo "true"; else echo "false"; fi),
    "jabali_config": $(if [[ -d /etc/jabali ]]; then echo "true"; else echo "false"; fi),
    "nginx_config": $(if [[ -f /etc/nginx/nginx.conf ]]; then echo "true"; else echo "false"; fi),
    "php_fpm_config": $(if ls /etc/php/*/fpm/php-fpm.conf &>/dev/null; then echo "true"; else echo "false"; fi),
    "frankenphp_config": $(if [[ -f /etc/jabali/Caddyfile ]]; then echo "true"; else echo "false"; fi),
    "panel_ssl": $(if [[ -d /etc/ssl/jabali ]]; then echo "true"; else echo "false"; fi),
    "stalwart_config": $(if [[ -d /etc/stalwart-mail ]]; then echo "true"; else echo "false"; fi),
    "stalwart_data": $(if [[ -d "${server_dir}/data/stalwart" ]]; then echo "true"; else echo "false"; fi),
    "redis_config": $(if [[ -f /etc/redis/redis.conf ]]; then echo "true"; else echo "false"; fi),
    "mariadb_config": $(if [[ -d /etc/mysql/mariadb.conf.d ]]; then echo "true"; else echo "false"; fi),
    "systemd_units": true,
    "letsencrypt": $(if [[ -d /etc/letsencrypt ]]; then echo "true"; else echo "false"; fi),
    "bulwark": $(if [[ -f "${server_dir}/metadata/bulwark.json" ]]; then echo '"metadata-only"'; elif [[ -d /opt/bulwark ]]; then echo '"inline"'; else echo '"missing"'; fi),
    "agent_addons": ${agent_addons},
    "restic_password": $(if [[ -f /etc/jabali/restic-password ]]; then echo "true"; else echo "false"; fi)
  }
}
EOMANIFEST

    log_info "server: Generated server manifest"
}
