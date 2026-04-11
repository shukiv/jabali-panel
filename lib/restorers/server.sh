#!/usr/bin/env bash
# Restorer: Full server restore for disaster recovery
# 5-phase restore per backup-server-spec.md

restore_server() {
    local extract_dir="$1" force="${2:-0}" skip_users="${3:-0}"
    local server_dir="${extract_dir}/server"

    if [[ ! -d "$server_dir" ]]; then
        log_error "restore/server: No server data found in snapshot"
        return 1
    fi

    log_info "restore/server: Starting full server restore"

    # Phase 1: Base install (env + jabali config)
    _restore_server_phase1 "$server_dir" "$force"

    # Phase 2: Databases
    _restore_server_phase2 "$server_dir" "$force"

    # Phase 3: Service configs
    _restore_server_phase3 "$server_dir" "$force"

    # Phase 4: User accounts
    if [[ "$skip_users" -eq 0 ]]; then
        _restore_server_phase4 "$extract_dir" "$force"
    else
        log_info "restore/server: Skipping user accounts (--skip-users)"
    fi

    # Phase 5: Finalize
    _restore_server_phase5 "$server_dir" "$force"

    log_info "restore/server: Full server restore complete"
}

# ── Phase 1: Base Install ──

_restore_server_phase1() {
    local server_dir="$1" force="$2"
    log_info "restore/server: ── Phase 1: Base install ──"

    local jabali_path="${CFG_JABALI_PATH:-/var/www/jabali}"

    # Restore .env
    if [[ -f "${server_dir}/panel/env" ]]; then
        if [[ -f "${jabali_path}/.env" ]] && [[ "$force" -eq 0 ]]; then
            log_info "restore/server: .env exists, skipping (use --force)"
        else
            cp "${server_dir}/panel/env" "${jabali_path}/.env"
            chmod 600 "${jabali_path}/.env"
            chown www-data:www-data "${jabali_path}/.env"
            log_info "restore/server: Restored .env"
        fi
    fi

    # Restore panel storage (branding, uploads)
    if [[ -d "${server_dir}/panel/storage/app" ]]; then
        mkdir -p "${jabali_path}/storage/app"
        rsync -a "${server_dir}/panel/storage/app/" "${jabali_path}/storage/app/"
        chown -R www-data:www-data "${jabali_path}/storage/app"
        log_info "restore/server: Restored panel storage"
    fi

    # Restore /etc/jabali/
    if [[ -d "${server_dir}/config/jabali" ]]; then
        mkdir -p /etc/jabali
        rsync -a "${server_dir}/config/jabali/" /etc/jabali/
        chmod 600 /etc/jabali/restic-password 2>/dev/null || true
        chmod 600 /etc/jabali/stalwart-api.conf 2>/dev/null || true
        log_info "restore/server: Restored /etc/jabali/"
    fi
}

# ── Phase 2: Databases ──

_restore_server_phase2() {
    local server_dir="$1" force="$2"
    log_info "restore/server: ── Phase 2: Databases ──"

    local db_dir="${server_dir}/databases"

    # Import jabali database
    if [[ -f "${db_dir}/jabali.sql.gz" ]]; then
        if [[ "$force" -eq 1 ]]; then
            log_info "restore/server: Importing jabali database (overwriting)"
        else
            log_info "restore/server: Importing jabali database"
        fi
        if gunzip -c "${db_dir}/jabali.sql.gz" | _mysql_root jabali 2>/dev/null; then
            log_info "restore/server: Imported jabali database"
        else
            log_warn "restore/server: Failed to import jabali database"
        fi
    fi

    # Import powerdns database
    if [[ -f "${db_dir}/powerdns.sql.gz" ]]; then
        _mysql_root_write "CREATE DATABASE IF NOT EXISTS powerdns" 2>/dev/null || true
        if gunzip -c "${db_dir}/powerdns.sql.gz" | _mysql_root powerdns 2>/dev/null; then
            log_info "restore/server: Imported powerdns database"
        else
            log_warn "restore/server: Failed to import powerdns database"
        fi
    fi

    # Run migrations
    local jabali_path="${CFG_JABALI_PATH:-/var/www/jabali}"
    if [[ -f "${jabali_path}/artisan" ]]; then
        php "${jabali_path}/artisan" migrate --force 2>/dev/null && \
            log_info "restore/server: Ran database migrations" || \
            log_warn "restore/server: Migration failed (may need manual review)"
    fi
}

# ── Phase 3: Service Configs ──

_restore_server_phase3() {
    local server_dir="$1" force="$2"
    log_info "restore/server: ── Phase 3: Service configs ──"

    # Nginx
    if [[ -d "${server_dir}/config/nginx" ]]; then
        [[ -f "${server_dir}/config/nginx/nginx.conf" ]] && \
            cp "${server_dir}/config/nginx/nginx.conf" /etc/nginx/nginx.conf
        [[ -d "${server_dir}/config/nginx/jabali" ]] && \
            rsync -a "${server_dir}/config/nginx/jabali/" /etc/nginx/jabali/
        if [[ -d "${server_dir}/config/nginx/sites-available" ]]; then
            rsync -a "${server_dir}/config/nginx/sites-available/" /etc/nginx/sites-available/
            # Recreate enabled symlinks
            if [[ -f "${server_dir}/config/nginx/sites-enabled.txt" ]]; then
                while IFS= read -r site; do
                    [[ -z "$site" ]] && continue
                    ln -sf "/etc/nginx/sites-available/${site}" "/etc/nginx/sites-enabled/${site}" 2>/dev/null
                done < "${server_dir}/config/nginx/sites-enabled.txt"
            fi
        fi
        nginx -t 2>/dev/null && log_info "restore/server: Nginx config restored and valid" \
            || log_warn "restore/server: Nginx config restored but validation failed"
    fi

    # PHP-FPM
    if [[ -d "${server_dir}/config/php" ]]; then
        for ver_dir in "${server_dir}/config/php"/*/; do
            [[ ! -d "$ver_dir" ]] && continue
            local ver
            ver=$(basename "$ver_dir")
            local fpm_dest="/etc/php/${ver}/fpm"
            [[ ! -d "$fpm_dest" ]] && continue
            [[ -f "${ver_dir}/php-fpm.conf" ]] && cp "${ver_dir}/php-fpm.conf" "$fpm_dest/"
            [[ -f "${ver_dir}/php.ini" ]] && cp "${ver_dir}/php.ini" "$fpm_dest/"
            [[ -f "${ver_dir}/www.conf" ]] && cp "${ver_dir}/www.conf" "$fpm_dest/pool.d/"
            [[ -f "${ver_dir}/admin.conf" ]] && cp "${ver_dir}/admin.conf" "$fpm_dest/pool.d/"
        done
        log_info "restore/server: PHP-FPM config restored"
    fi

    # Stalwart mail
    if [[ -d "${server_dir}/config/stalwart" ]]; then
        mkdir -p /etc/stalwart-mail
        rsync -a "${server_dir}/config/stalwart/" /etc/stalwart-mail/
        log_info "restore/server: Stalwart config restored"
    fi

    # Redis
    if [[ -f "${server_dir}/config/redis/redis.conf" ]]; then
        cp "${server_dir}/config/redis/redis.conf" /etc/redis/redis.conf
        chmod 640 /etc/redis/redis.conf
        chown redis:redis /etc/redis/redis.conf 2>/dev/null || true
        log_info "restore/server: Redis config restored"
    fi

    # MariaDB
    if [[ -d "${server_dir}/config/mysql" ]]; then
        [[ -d "${server_dir}/config/mysql/mariadb.conf.d" ]] && \
            rsync -a "${server_dir}/config/mysql/mariadb.conf.d/" /etc/mysql/mariadb.conf.d/
        if [[ -f "${server_dir}/config/mysql/debian.cnf" ]]; then
            cp "${server_dir}/config/mysql/debian.cnf" /etc/mysql/debian.cnf
            chmod 600 /etc/mysql/debian.cnf
        fi
        log_info "restore/server: MariaDB config restored"
    fi

    # Panel SSL
    if [[ -d "${server_dir}/ssl/jabali" ]]; then
        mkdir -p /etc/ssl/jabali
        rsync -a "${server_dir}/ssl/jabali/" /etc/ssl/jabali/
        chmod 600 /etc/ssl/jabali/*.key 2>/dev/null || true
        log_info "restore/server: Panel SSL cert restored"
    fi

    # Systemd units
    if [[ -d "${server_dir}/systemd" ]]; then
        local unit_count=0
        for unit in "${server_dir}/systemd"/*.service "${server_dir}/systemd"/*.timer "${server_dir}/systemd"/*.slice; do
            [[ ! -f "$unit" ]] && continue
            cp "$unit" /etc/systemd/system/
            unit_count=$((unit_count + 1))
        done
        [[ -d "${server_dir}/systemd/nginx.service.d" ]] && \
            cp -a "${server_dir}/systemd/nginx.service.d" /etc/systemd/system/
        log_info "restore/server: Restored $unit_count systemd unit(s)"
    fi

    # Let's Encrypt
    if [[ -d "${server_dir}/letsencrypt" ]]; then
        mkdir -p /etc/letsencrypt
        rsync -a "${server_dir}/letsencrypt/" /etc/letsencrypt/
        log_info "restore/server: Restored Let's Encrypt state"
    fi
}

# ── Phase 4: User Accounts ──

_restore_server_phase4() {
    local extract_dir="$1" force="$2"
    log_info "restore/server: ── Phase 4: User accounts ──"

    # Find all user snapshots and restore each
    local accounts
    accounts=$(discover_all_accounts 2>/dev/null)
    if [[ -z "$accounts" ]]; then
        log_info "restore/server: No user accounts found in database"
        return 0
    fi

    local user_count=0
    while IFS=$'\t' read -r uid username _ _; do
        [[ -z "$username" ]] && continue
        log_info "restore/server: Restoring user $username"
        # Each user was backed up as a separate snapshot — restore from latest
        cmd_restore "$username" --snapshot=latest --force 2>/dev/null || \
            log_warn "restore/server: Failed to restore user $username"
        user_count=$((user_count + 1))
    done <<< "$accounts"

    log_info "restore/server: Restored $user_count user account(s)"
}

# ── Phase 5: Finalize ──

_restore_server_phase5() {
    local server_dir="$1" force="$2"
    log_info "restore/server: ── Phase 5: Finalize ──"

    # Agent addons
    if [[ -d "${server_dir}/config/jabali/agent.d" ]]; then
        mkdir -p /etc/jabali/agent.d
        rsync -a "${server_dir}/config/jabali/agent.d/" /etc/jabali/agent.d/
        log_info "restore/server: Restored agent addons"
    fi

    # Reload systemd
    systemctl daemon-reload 2>/dev/null

    # Restart services
    local services=(mariadb redis-server nginx stalwart-mail)
    for svc in "${services[@]}"; do
        if systemctl is-enabled "$svc" &>/dev/null; then
            systemctl restart "$svc" 2>/dev/null && \
                log_info "restore/server: Restarted $svc" || \
                log_warn "restore/server: Failed to restart $svc"
        fi
    done

    # Restart PHP-FPM (all versions)
    for fpm in /etc/systemd/system/multi-user.target.wants/php*-fpm.service \
               /lib/systemd/system/php*-fpm.service; do
        [[ ! -f "$fpm" ]] && continue
        local fpm_name
        fpm_name=$(basename "$fpm")
        systemctl restart "$fpm_name" 2>/dev/null && \
            log_info "restore/server: Restarted $fpm_name" || true
    done

    # Restart Jabali services
    for jsvc in jabali-panel jabali-agent jabali-queue; do
        if systemctl is-enabled "$jsvc" &>/dev/null; then
            systemctl restart "$jsvc" 2>/dev/null && \
                log_info "restore/server: Restarted $jsvc" || \
                log_warn "restore/server: Failed to restart $jsvc"
        fi
    done

    # Post-restore checks
    _server_post_restore_checks
}

_server_post_restore_checks() {
    log_info "restore/server: ── Post-restore checks ──"
    local passed=0 failed=0

    # Service checks
    for svc in jabali-panel jabali-agent nginx mariadb redis-server; do
        if systemctl is-active "$svc" &>/dev/null; then
            log_info "restore/server: ✓ $svc is running"
            passed=$((passed + 1))
        else
            log_warn "restore/server: ✗ $svc is not running"
            failed=$((failed + 1))
        fi
    done

    # Nginx config valid
    if nginx -t 2>/dev/null; then
        log_info "restore/server: ✓ nginx -t passed"
        passed=$((passed + 1))
    else
        log_warn "restore/server: ✗ nginx -t failed"
        failed=$((failed + 1))
    fi

    # Panel accessible
    local panel_port
    panel_port=$(grep -oP 'PANEL_PORT=\K\d+' /var/www/jabali/.env 2>/dev/null || echo "8443")
    if curl -sk -o /dev/null -w '%{http_code}' "https://localhost:${panel_port}/jabali-admin/login" 2>/dev/null | grep -q "200\|302"; then
        log_info "restore/server: ✓ Panel is accessible"
        passed=$((passed + 1))
    else
        log_warn "restore/server: ✗ Panel not accessible on port $panel_port"
        failed=$((failed + 1))
    fi

    # Database accessible
    if _mysql_root_query "SELECT COUNT(*) FROM users" 2>/dev/null | grep -q "[0-9]"; then
        log_info "restore/server: ✓ Database accessible"
        passed=$((passed + 1))
    else
        log_warn "restore/server: ✗ Database not accessible"
        failed=$((failed + 1))
    fi

    # DNS responding
    if command -v dig &>/dev/null && dig @localhost localhost +short &>/dev/null; then
        log_info "restore/server: ✓ PowerDNS responding"
        passed=$((passed + 1))
    fi

    log_info "restore/server: Post-restore: $passed passed, $failed failed"
}
