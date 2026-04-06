#!/usr/bin/env bash
# Collector: Nginx vhost configs and hotlink protection

collect_nginx() {
    local user_id="$1" staging_dir="$2"
    local nginx_dir="${staging_dir}/nginx"

    local domains
    domains=$(discover_domains "$user_id")
    [[ -z "$domains" ]] && return 0

    mkdir -p "$nginx_dir"

    local count=0
    while IFS=$'\t' read -r domain_id domain_name doc_root ssl_en custom_directives; do
        [[ -z "$domain_id" ]] && continue

        # Copy nginx config files
        local found=0
        for conf in "${CFG_NGINX_SITES}/${domain_name}"*; do
            if [[ -f "$conf" ]]; then
                cp "$conf" "${nginx_dir}/$(basename "$conf")"
                found=1
            fi
        done

        # Export custom directives from DB
        if [[ -n "$custom_directives" ]] && [[ "$custom_directives" != "NULL" ]]; then
            echo "$custom_directives" > "${nginx_dir}/${domain_name}.custom-directives"
        fi

        # Hotlink protection
        local hotlink
        hotlink=$(_db_query "SELECT is_enabled, allowed_domains, block_blank_referrer, protected_extensions, redirect_url FROM domain_hotlink_settings WHERE domain_id = $domain_id" 2>/dev/null)
        if [[ -n "$hotlink" ]]; then
            IFS=$'\t' read -r hl_enabled hl_allowed hl_blank hl_ext hl_redirect <<< "$hotlink"
            cat > "${nginx_dir}/${domain_name}.hotlink.json" <<EOJSON
{
  "is_enabled": ${hl_enabled:-0},
  "allowed_domains": "${hl_allowed}",
  "block_blank_referrer": ${hl_blank:-0},
  "protected_extensions": "${hl_ext}",
  "redirect_url": "${hl_redirect}"
}
EOJSON
        fi

        if [[ "$found" -eq 1 ]]; then
            count=$((count + 1))
            log_info "nginx: Exported config for ${domain_name}"
        fi
    done <<< "$domains"

    log_info "nginx: Exported $count domain configs"
}
