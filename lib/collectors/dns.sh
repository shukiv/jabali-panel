#!/usr/bin/env bash
# Collector: DNS records from PowerDNS via Jabali DB

collect_dns() {
    local user_id="$1" staging_dir="$2"
    local dns_dir="${staging_dir}/dns"

    # Get domains for this user
    local domains
    domains=$(discover_domains "$user_id")
    [[ -z "$domains" ]] && return 0

    mkdir -p "$dns_dir"

    local count=0
    while IFS=$'\t' read -r domain_id domain_name _ _ _; do
        [[ -z "$domain_id" ]] && continue

        local records
        records=$(discover_dns_records "$domain_id")
        [[ -z "$records" ]] && continue

        # JSON export (lossless)
        local json_file="${dns_dir}/${domain_name}.json"
        echo "[" > "$json_file"
        local first=1
        while IFS=$'\t' read -r name type content ttl priority; do
            [[ -z "$name" ]] && continue
            [[ "$first" -eq 0 ]] && echo "," >> "$json_file"
            first=0
            printf '  {"name":"%s","type":"%s","content":"%s","ttl":%s,"priority":%s}' \
                "$name" "$type" "${content//\"/\\\"}" "${ttl:-3600}" "${priority:-0}" >> "$json_file"
        done <<< "$records"
        echo -e "\n]" >> "$json_file"

        # BIND zone file export
        local zone_file="${dns_dir}/${domain_name}.zone"
        {
            echo "; Zone file for ${domain_name}"
            echo "; Exported by jabali-backup on $(date -Iseconds)"
            echo "\$ORIGIN ${domain_name}."
            echo "\$TTL 3600"
            while IFS=$'\t' read -r name type content ttl priority; do
                [[ -z "$name" ]] && continue
                local rname="${name}"
                [[ "$rname" == "@" ]] && rname="${domain_name}."
                case "$type" in
                    MX|SRV)
                        printf "%-30s %s IN %-6s %s %s\n" "$rname" "${ttl:-3600}" "$type" "${priority:-10}" "$content"
                        ;;
                    *)
                        printf "%-30s %s IN %-6s %s\n" "$rname" "${ttl:-3600}" "$type" "$content"
                        ;;
                esac
            done <<< "$records"
        } > "$zone_file"

        count=$((count + 1))
        log_info "dns: Exported ${domain_name} ($(echo "$records" | wc -l) records)"
    done <<< "$domains"

    log_info "dns: Exported $count zones"
}
