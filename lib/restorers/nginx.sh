#!/usr/bin/env bash
# Restorer: nginx vhost configs and hotlink protection

restore_nginx() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/nginx"

    [[ ! -d "$staging" ]] && return 0

    local count=0 need_reload=0
    for conf in "${staging}"/*.conf; do
        [[ ! -f "$conf" ]] && continue
        local fname
        fname=$(basename "$conf")
        local target="${CFG_NGINX_SITES}/${fname}"

        if [[ -f "$target" ]] && [[ "$force" -eq 0 ]]; then
            log_info "restore/nginx: $fname exists, skipping"
            continue
        fi

        cp "$conf" "$target"
        # Create symlink in sites-enabled (panel convention: config in sites-available, symlink in sites-enabled)
        ln -sf "$target" "/etc/nginx/sites-enabled/${fname}"
        count=$((count + 1))
        need_reload=1
        log_info "restore/nginx: Restored $fname"
    done

    # Restore hotlink settings
    for hotlink_json in "${staging}"/*.hotlink.json; do
        [[ ! -f "$hotlink_json" ]] && continue
        local domain_name
        domain_name=$(basename "$hotlink_json" .hotlink.json)
        local domain_id
        domain_id=$(_db_query "SELECT id FROM domains WHERE domain='$(mysql_escape "$domain_name")' LIMIT 1")
        [[ -z "$domain_id" ]] && continue

        local hl_enabled hl_allowed hl_blank hl_ext hl_redirect
        hl_enabled=$(grep -oP '"is_enabled"\s*:\s*\K[0-9]+' "$hotlink_json" || echo "0")
        hl_allowed=$(grep -oP '"allowed_domains"\s*:\s*"\K[^"]*' "$hotlink_json" || true)
        hl_ext=$(grep -oP '"protected_extensions"\s*:\s*"\K[^"]*' "$hotlink_json" || true)
        hl_redirect=$(grep -oP '"redirect_url"\s*:\s*"\K[^"]*' "$hotlink_json" || true)
        hl_blank=$(grep -oP '"block_blank_referrer"\s*:\s*\K[0-9]+' "$hotlink_json" || echo "0")

        local exists
        exists=$(_db_query "SELECT 1 FROM domain_hotlink_settings WHERE domain_id=$domain_id LIMIT 1")
        if [[ -z "$exists" ]] || [[ "$force" -eq 1 ]]; then
            [[ -n "$exists" ]] && _db_write "DELETE FROM domain_hotlink_settings WHERE domain_id=$domain_id"
            _db_write "INSERT INTO domain_hotlink_settings (domain_id, is_enabled, allowed_domains, block_blank_referrer, protected_extensions, redirect_url, created_at, updated_at) VALUES ($domain_id, $hl_enabled, '$(mysql_escape "$hl_allowed")', $hl_blank, '$(mysql_escape "$hl_ext")', '$(mysql_escape "$hl_redirect")', NOW(), NOW())"
        fi
    done

    # Restore cache zone config
    for cache_conf in "${staging}"/*.cache-zone.conf; do
        [[ ! -f "$cache_conf" ]] && continue
        local cache_dir="/etc/nginx/jabali/cache-zones"
        mkdir -p "$cache_dir" 2>/dev/null
        local cache_target="${cache_dir}/$(basename "$cache_conf" .cache-zone.conf).conf"
        if [[ -f "$cache_target" ]] && [[ "$force" -eq 0 ]]; then
            log_info "restore/nginx: Cache zone $(basename "$cache_target") exists, skipping"
        else
            cp "$cache_conf" "$cache_target"
            need_reload=1
            log_info "restore/nginx: Restored cache zone $(basename "$cache_target")"
        fi
        # Ensure cache directory exists
        mkdir -p "/home/${username}/cache/nginx" 2>/dev/null || true
    done

    if [[ "$need_reload" -eq 1 ]]; then
        if nginx -t 2>/dev/null; then
            nginx -s reload 2>/dev/null && log_info "restore/nginx: Reloaded nginx"
        else
            log_warn "restore/nginx: nginx config test failed, skipping reload"
        fi
    fi

    log_info "restore/nginx: Restored $count config(s)"
}
