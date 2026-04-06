#!/usr/bin/env bash
# Restorer: DNS records from JSON back to Jabali DB

restore_dns() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/dns"

    [[ ! -d "$staging" ]] && return 0

    local count=0
    for json_file in "${staging}"/*.json; do
        [[ ! -f "$json_file" ]] && continue
        local domain_name
        domain_name=$(basename "$json_file" .json)

        # Look up domain_id
        local domain_id
        domain_id=$(_db_query "SELECT id FROM domains WHERE domain='$(mysql_escape "$domain_name")' LIMIT 1")
        if [[ -z "$domain_id" ]]; then
            log_warn "restore/dns: Domain $domain_name not found in DB, skipping"
            continue
        fi

        if [[ "$force" -eq 1 ]]; then
            _db_write "DELETE FROM dns_records WHERE domain_id=$domain_id"
            log_info "restore/dns: Cleared existing records for $domain_name"
        fi

        # Parse JSON array and insert records
        # Each line: {"name":"...","type":"...","content":"...","ttl":...,"priority":...}
        local inserted=0
        while IFS= read -r line; do
            local rname rtype rcontent rttl rpriority
            rname=$(echo "$line" | grep -oP '"name"\s*:\s*"\K[^"]+' || true)
            rtype=$(echo "$line" | grep -oP '"type"\s*:\s*"\K[^"]+' || true)
            rcontent=$(echo "$line" | grep -oP '"content"\s*:\s*"\K[^"]+' || true)
            rttl=$(echo "$line" | grep -oP '"ttl"\s*:\s*\K[0-9]+' || true)
            rpriority=$(echo "$line" | grep -oP '"priority"\s*:\s*\K[0-9]+' || true)

            [[ -z "$rname" || -z "$rtype" ]] && continue

            # Skip duplicates in merge mode
            if [[ "$force" -eq 0 ]]; then
                local exists
                exists=$(_db_query "SELECT 1 FROM dns_records WHERE domain_id=$domain_id AND name='$(mysql_escape "$rname")' AND type='$(mysql_escape "$rtype")' AND content='$(mysql_escape "$rcontent")' LIMIT 1")
                [[ -n "$exists" ]] && continue
            fi

            _db_write "INSERT INTO dns_records (domain_id, name, type, content, ttl, priority, created_at, updated_at) VALUES ($domain_id, '$(mysql_escape "$rname")', '$(mysql_escape "$rtype")', '$(mysql_escape "$rcontent")', ${rttl:-3600}, ${rpriority:-NULL}, NOW(), NOW())"
            inserted=$((inserted + 1))
        done < <(grep -oP '\{[^}]+\}' "$json_file")

        count=$((count + 1))
        log_info "restore/dns: Restored $inserted records for $domain_name"
    done

    log_info "restore/dns: Restored $count zone(s)"
}
