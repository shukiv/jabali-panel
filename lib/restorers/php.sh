#!/usr/bin/env bash
# Restorer: PHP-FPM pool configs

restore_php() {
    local extract_dir="$1" username="$2" force="${3:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/php"

    [[ ! -d "$staging" ]] && return 0

    local php_version=""
    [[ -f "${staging}/version" ]] && php_version=$(cat "${staging}/version")

    local count=0 need_reload=""
    for conf in "${staging}"/*.conf; do
        [[ ! -f "$conf" ]] && continue
        local fname
        fname=$(basename "$conf")

        # Find target pool directory
        local target_dir=""
        if [[ -n "$php_version" ]] && [[ -d "/etc/php/${php_version}/fpm/pool.d" ]]; then
            target_dir="/etc/php/${php_version}/fpm/pool.d"
        else
            # Find any PHP-FPM pool directory
            for d in /etc/php/*/fpm/pool.d; do
                [[ -d "$d" ]] && target_dir="$d" && break
            done
        fi

        [[ -z "$target_dir" ]] && { log_warn "restore/php: No PHP-FPM pool directory found"; continue; }

        local target="${target_dir}/${fname}"
        if [[ -f "$target" ]] && [[ "$force" -eq 0 ]]; then
            log_info "restore/php: $fname exists, skipping"
            continue
        fi

        cp "$conf" "$target"
        count=$((count + 1))
        need_reload="$php_version"
        log_info "restore/php: Restored $fname → $target"
    done

    if [[ -n "$need_reload" ]]; then
        local svc="php${need_reload}-fpm"
        if systemctl is-active "$svc" &>/dev/null; then
            systemctl reload "$svc" 2>/dev/null && log_info "restore/php: Reloaded $svc"
        fi
    fi

    log_info "restore/php: Restored $count pool config(s)"
}
