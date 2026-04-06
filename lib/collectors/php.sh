#!/usr/bin/env bash
# Collector: PHP-FPM pool configs

collect_php() {
    local username="$1" staging_dir="$2"
    local php_dir="${staging_dir}/php"

    # Find PHP-FPM pool configs for this user
    local found=0
    for pool_dir in $CFG_PHP_FPM_POOLS; do
        for conf in "${pool_dir}/${username}.conf" "${pool_dir}/${username}"_*.conf; do
            if [[ -f "$conf" ]]; then
                mkdir -p "$php_dir"
                cp "$conf" "${php_dir}/$(basename "$conf")"
                found=$((found + 1))
            fi
        done
    done

    # Detect PHP version from pool config
    if [[ "$found" -gt 0 ]]; then
        local php_version
        php_version=$(ls -d /etc/php/*/fpm/pool.d/${username}.conf 2>/dev/null | grep -oP '\d+\.\d+' | head -1)
        if [[ -n "$php_version" ]]; then
            echo "$php_version" > "${php_dir}/version"
        fi
        log_info "php: Exported $found pool config(s) for $username"
    else
        log_info "php: No PHP-FPM pool found for $username"
    fi
}
