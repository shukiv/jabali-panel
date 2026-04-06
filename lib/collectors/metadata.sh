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
  "git_deployments": ${git_json},
  "webhooks": ${webhooks_json}
}
EOJSON

    log_info "metadata: Exported account metadata for $username"
}
