#!/usr/bin/env bash
# Collector: WordPress site exports

collect_wordpress() {
    local username="$1" home_dir="$2" staging_dir="$3"
    local wp_dir="${staging_dir}/wordpress"

    [[ ! -d "$home_dir" ]] && return 0

    # Find wp-config.php files
    local wp_configs
    wp_configs=$(find "$home_dir" -maxdepth 4 -name "wp-config.php" -type f 2>/dev/null)
    [[ -z "$wp_configs" ]] && return 0

    local count=0
    while IFS= read -r config; do
        [[ -z "$config" ]] && continue
        local wp_root
        wp_root="$(dirname "$config")"
        local site_name
        site_name="$(basename "$(dirname "$config")")"
        [[ "$site_name" == "$username" ]] && site_name="main"

        local site_dir="${wp_dir}/${site_name}"
        mkdir -p "$site_dir"

        # Export with WP-CLI if available
        if command -v wp &>/dev/null; then
            # Plugin list
            sudo -u "$username" wp plugin list --path="$wp_root" --format=json 2>/dev/null \
                > "${site_dir}/plugins.json" || true
            # Theme list
            sudo -u "$username" wp theme list --path="$wp_root" --format=json 2>/dev/null \
                > "${site_dir}/themes.json" || true
            # Core version
            sudo -u "$username" wp core version --path="$wp_root" 2>/dev/null \
                > "${site_dir}/version" || true
            # Options (non-sensitive)
            sudo -u "$username" wp option list --path="$wp_root" --format=json \
                --fields=option_name,option_value \
                --search="siteurl,blogname,blogdescription,admin_email,permalink_structure,template,stylesheet" \
                2>/dev/null > "${site_dir}/options.json" || true
        fi

        count=$((count + 1))
        log_info "wordpress: Found WP install at $wp_root"
    done <<< "$wp_configs"

    [[ "$count" -gt 0 ]] && log_info "wordpress: Exported $count WordPress site(s)"
}
