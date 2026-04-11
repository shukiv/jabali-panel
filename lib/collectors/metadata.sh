#!/usr/bin/env bash
# Collector: Account metadata from Jabali DB

collect_metadata() {
    local user_id="$1" username="$2" staging_dir="$3"
    local meta_dir="${staging_dir}/metadata"
    mkdir -p "$meta_dir"

    # User record
    local user_data
    user_data=$(_db_query "SELECT u.id, u.username, u.email, u.is_admin, u.disk_quota_mb, u.ssh_isolation_mode, u.locale, hp.name as package_name, hp.disk_quota_mb as pkg_quota, hp.bandwidth_gb, hp.domains_limit, hp.databases_limit, hp.mailboxes_limit FROM users u LEFT JOIN hosting_packages hp ON u.hosting_package_id = hp.id WHERE u.id = $user_id")

    IFS=$'\t' read -r uid uname uemail is_admin disk_quota ssh_iso locale pkg_name pkg_quota bw_gb dom_lim db_lim mb_lim <<< "$user_data"

    # Domains
    local domains_json="[]"
    local domains
    domains=$(discover_domains "$user_id")
    if [[ -n "$domains" ]]; then
        domains_json="["
        local first=1
        while IFS=$'\t' read -r did dname doc_root ssl_en _; do
            [[ -z "$did" ]] && continue
            [[ "$first" -eq 0 ]] && domains_json+=","
            first=0
            domains_json+="$(printf '{"id":%s,"domain":"%s","document_root":"%s","ssl_enabled":%s}' \
                "$did" "$dname" "${doc_root//\"/\\\"}" "${ssl_en:-0}")"
        done <<< "$domains"
        domains_json+="]"
    fi

    # Domain aliases
    local aliases_json="[]"
    local aliases
    aliases=$(discover_domain_aliases "$user_id")
    if [[ -n "$aliases" ]]; then
        aliases_json="["
        local first=1
        while IFS=$'\t' read -r alias_name parent_domain; do
            [[ -z "$alias_name" ]] && continue
            [[ "$first" -eq 0 ]] && aliases_json+=","
            first=0
            aliases_json+="$(printf '{"alias":"%s","domain":"%s"}' "$alias_name" "$parent_domain")"
        done <<< "$aliases"
        aliases_json+="]"
    fi

    # Git deployments
    local git_json="[]"
    local git_data
    git_data=$(_db_query "SELECT id, repo_url, branch, deploy_path, auto_deploy FROM git_deployments WHERE user_id = $user_id" 2>/dev/null)
    if [[ -n "$git_data" ]]; then
        git_json="["
        local first=1
        while IFS=$'\t' read -r gid repo branch dpath auto; do
            [[ -z "$gid" ]] && continue
            [[ "$first" -eq 0 ]] && git_json+=","
            first=0
            git_json+="$(printf '{"id":%s,"repo_url":"%s","branch":"%s","deploy_path":"%s","auto_deploy":%s}' \
                "$gid" "${repo//\"/\\\"}" "$branch" "${dpath//\"/\\\"}" "${auto:-0}")"
        done <<< "$git_data"
        git_json+="]"
    fi

    # Webhooks
    local webhooks_json="[]"
    local wh_data
    wh_data=$(_db_query "SELECT id, name, url, events, is_active FROM webhook_endpoints WHERE user_id = $user_id" 2>/dev/null)
    if [[ -n "$wh_data" ]]; then
        webhooks_json="["
        local first=1
        while IFS=$'\t' read -r wid wname wurl wevents wactive; do
            [[ -z "$wid" ]] && continue
            [[ "$first" -eq 0 ]] && webhooks_json+=","
            first=0
            webhooks_json+="$(printf '{"id":%s,"name":"%s","url":"%s","events":%s,"is_active":%s}' \
                "$wid" "${wname//\"/\\\"}" "${wurl//\"/\\\"}" "${wevents:-\"[]\"}" "${wactive:-1}")"
        done <<< "$wh_data"
        webhooks_json+="]"
    fi

    # MySQL credentials
    local mysql_cred_json="null"
    local mysql_cred
    mysql_cred=$(_db_query "SELECT mysql_username FROM mysql_credentials WHERE user_id = $user_id")
    if [[ -n "$mysql_cred" ]]; then
        mysql_cred_json="$(printf '{"mysql_username":"%s"}' "$mysql_cred")"
    fi

    # Domain redirects
    local redirects_json="[]"
    local redirect_data
    redirect_data=$(_db_query "SELECT dr.id, d.domain, dr.source_path, dr.target_url, dr.redirect_type, dr.is_active FROM domain_redirects dr JOIN domains d ON dr.domain_id=d.id WHERE d.user_id = $user_id" 2>/dev/null)
    if [[ -n "$redirect_data" ]]; then
        redirects_json="["
        local first=1
        while IFS=$'\t' read -r rid rdomain rsrc rtgt rtype ractive; do
            [[ -z "$rid" ]] && continue
            [[ "$first" -eq 0 ]] && redirects_json+=","
            first=0
            redirects_json+="$(printf '{"id":%s,"domain":"%s","source_path":"%s","target_url":"%s","redirect_type":%s,"is_active":%s}' \
                "$rid" "$rdomain" "${rsrc//\"/\\\"}" "${rtgt//\"/\\\"}" "${rtype:-301}" "${ractive:-1}")"
        done <<< "$redirect_data"
        redirects_json+="]"
    fi

    # User settings
    local settings_json="[]"
    local settings_data
    settings_data=$(_db_query "SELECT \`key\`, value FROM user_settings WHERE user_id = $user_id" 2>/dev/null)
    if [[ -n "$settings_data" ]]; then
        settings_json="["
        local first=1
        while IFS=$'\t' read -r skey sval; do
            [[ -z "$skey" ]] && continue
            [[ "$first" -eq 0 ]] && settings_json+=","
            first=0
            settings_json+="$(printf '{"key":"%s","value":"%s"}' "${skey//\"/\\\"}" "${sval//\"/\\\"}")"
        done <<< "$settings_data"
        settings_json+="]"
    fi

    # IMAP sync tasks
    local imap_json="[]"
    local imap_data
    imap_data=$(_db_query "SELECT id, source_host, source_port, source_username, source_encryption, destination_mailbox_id, is_active, status FROM imap_sync_tasks WHERE user_id = $user_id" 2>/dev/null)
    if [[ -n "$imap_data" ]]; then
        imap_json="["
        local first=1
        while IFS=$'\t' read -r iid ihost iport iuser ienc idest iactive istatus; do
            [[ -z "$iid" ]] && continue
            [[ "$first" -eq 0 ]] && imap_json+=","
            first=0
            imap_json+="$(printf '{"id":%s,"source_host":"%s","source_port":%s,"source_username":"%s","source_encryption":"%s","destination_mailbox_id":%s,"is_active":%s,"status":"%s"}' \
                "$iid" "${ihost//\"/\\\"}" "${iport:-993}" "${iuser//\"/\\\"}" "${ienc:-ssl}" "${idest:-0}" "${iactive:-0}" "${istatus:-pending}")"
        done <<< "$imap_data"
        imap_json+="]"
    fi

    # Cloudflare zones
    local cf_json="[]"
    local cf_data
    cf_data=$(_db_query "SELECT id, domain_id, zone_id FROM cloudflare_zones WHERE user_id = $user_id" 2>/dev/null)
    if [[ -n "$cf_data" ]]; then
        cf_json="["
        local first=1
        while IFS=$'\t' read -r cfid cfdid cfzid; do
            [[ -z "$cfid" ]] && continue
            [[ "$first" -eq 0 ]] && cf_json+=","
            first=0
            cf_json+="$(printf '{"id":%s,"domain_id":%s,"zone_id":"%s"}' "$cfid" "$cfdid" "$cfzid")"
        done <<< "$cf_data"
        cf_json+="]"
    fi

    # System user info (UID, GID, shell, groups)
    local sys_uid="" sys_gid="" sys_shell="" sys_groups=""
    if id "$username" &>/dev/null; then
        sys_uid=$(id -u "$username" 2>/dev/null)
        sys_gid=$(id -g "$username" 2>/dev/null)
        sys_shell=$(getent passwd "$username" 2>/dev/null | cut -d: -f7)
        sys_groups=$(id -Gn "$username" 2>/dev/null | tr ' ' ',')
        # Write system_user.json separately (used by restorer for UID/GID preservation)
        cat > "${meta_dir}/system_user.json" <<EOSYS
{"uid":${sys_uid},"gid":${sys_gid},"shell":"${sys_shell}","groups":"${sys_groups}"}
EOSYS
    fi

    # Write account.json
    cat > "${meta_dir}/account.json" <<EOJSON
{
  "backup_tool_version": "${JABALI_BACKUP_VERSION:-dev}",
  "backup_date": "$(date -Iseconds)",
  "hostname": "$(hostname)",
  "user": {
    "id": ${uid:-0},
    "username": "${uname}",
    "email": "${uemail}",
    "is_admin": ${is_admin:-0},
    "disk_quota_mb": ${disk_quota:-0},
    "ssh_isolation_mode": "${ssh_iso}",
    "locale": "${locale}"
  },
  "hosting_package": {
    "name": "${pkg_name}",
    "disk_quota_mb": ${pkg_quota:-0},
    "bandwidth_gb": ${bw_gb:-0},
    "domains_limit": ${dom_lim:-0},
    "databases_limit": ${db_lim:-0},
    "mailboxes_limit": ${mb_lim:-0}
  },
  "mysql_credentials": ${mysql_cred_json},
  "domains": ${domains_json},
  "domain_aliases": ${aliases_json},
  "domain_redirects": ${redirects_json},
  "git_deployments": ${git_json},
  "webhooks": ${webhooks_json},
  "cloudflare_zones": ${cf_json},
  "user_settings": ${settings_json},
  "imap_sync_tasks": ${imap_json}
}
EOJSON

    log_info "metadata: Exported account metadata for $username"
}
