#!/usr/bin/env bash
# Collector: Stalwart mail server account export via stalwart-cli
#
# Uses `stalwart-cli export account` to capture the full JMAP account:
#   - Emails and mailbox structure
#   - Sieve scripts
#   - Identities
#   - Vacation responses
#
# Falls back to the Stalwart REST API if stalwart-cli is not available.
# Config: [stalwart] section in config.conf
#
# Relationship with email collector:
#   The email collector handles Jabali DB metadata (domains, mailboxes,
#   forwarders, autoresponders, shares). This collector handles mail content
#   and JMAP state. When both are enabled, the email collector skips maildir
#   paths to avoid duplicating mail content.

collect_stalwart() {
    local user_id="$1" staging_dir="$2"
    local stalwart_dir="${staging_dir}/stalwart"

    # Check if Stalwart backup is enabled
    [[ "$CFG_STALWART_ENABLED" != "true" ]] && return 0

    # Verify Stalwart is reachable before exporting
    if ! _stalwart_ping; then
        log_error "stalwart: Cannot reach Stalwart at ${CFG_STALWART_URL} — skipping"
        return 1
    fi

    # Discover mailboxes for this user to get Stalwart account names
    local mailboxes
    mailboxes=$(discover_mailboxes "$user_id")
    [[ -z "$mailboxes" ]] && return 0

    mkdir -p "$stalwart_dir"

    # Collect unique email addresses (= Stalwart account names)
    local -A exported_accounts
    while IFS=$'\t' read -r mb_id local_part domain _rest; do
        [[ -z "$mb_id" ]] && continue
        local account="${local_part}@${domain}"

        # Skip if already exported (same account can appear via multiple domains)
        [[ -n "${exported_accounts[$account]:-}" ]] && continue
        exported_accounts[$account]=1

        # Dry-run: just log what would be exported
        if [[ "${JABALI_DRY_RUN:-0}" -eq 1 ]]; then
            log_info "stalwart: Would export account $account (JMAP export — replaces maildir)"
            continue
        fi

        local account_dir="${stalwart_dir}/${account}"
        mkdir -p "$account_dir"

        if _stalwart_export_cli "$account" "$account_dir"; then
            log_info "stalwart: Exported account $account via CLI"
        elif _stalwart_export_api "$account" "$account_dir"; then
            log_info "stalwart: Exported account $account via API"
        else
            log_warn "stalwart: Failed to export account $account"
            continue
        fi
    done <<< "$mailboxes"

    # Export Stalwart principal metadata (domains, groups)
    if [[ "${JABALI_DRY_RUN:-0}" -ne 1 ]]; then
        _stalwart_export_principals "$stalwart_dir"
    fi
}

# Verify Stalwart is reachable
_stalwart_ping() {
    [[ -z "$CFG_STALWART_URL" ]] && return 1
    [[ -z "$CFG_STALWART_ADMIN_TOKEN" ]] && return 1

    curl -sfS -H "Authorization: Bearer ${CFG_STALWART_ADMIN_TOKEN}" \
        -H 'Accept: application/json' \
        "${CFG_STALWART_URL%/}/api/principal?limit=1" >/dev/null 2>&1
}

# Export via stalwart-cli (preferred — captures everything)
_stalwart_export_cli() {
    local account="$1" dest_dir="$2"

    command -v stalwart-cli &>/dev/null || return 1

    local cli_args=()
    cli_args+=(-u "$CFG_STALWART_URL")
    if [[ -n "$CFG_STALWART_ADMIN_TOKEN" ]]; then
        cli_args+=(-c "$CFG_STALWART_ADMIN_TOKEN")
    fi

    stalwart-cli "${cli_args[@]}" export account "$account" "$dest_dir" \
        --num-concurrent 4 2>/dev/null
}

# Export via REST API (fallback — captures principal + mailbox metadata)
_stalwart_export_api() {
    local account="$1" dest_dir="$2"

    [[ -z "$CFG_STALWART_URL" ]] && return 1
    [[ -z "$CFG_STALWART_ADMIN_TOKEN" ]] && return 1

    local base_url="${CFG_STALWART_URL%/}"
    local auth_header="Authorization: Bearer ${CFG_STALWART_ADMIN_TOKEN}"

    # Fetch principal details
    local principal
    principal=$(curl -sfS -H "$auth_header" -H 'Accept: application/json' \
        "${base_url}/api/principal/${account}" 2>/dev/null) || return 1

    local data
    data=$(echo "$principal" | jq -r '.data // empty' 2>/dev/null)
    [[ -z "$data" || "$data" == "null" ]] && return 1

    echo "$data" > "${dest_dir}/principal.json"
    log_info "stalwart: Fetched principal for $account via API"

    return 0
}

# Export domain and group principals
_stalwart_export_principals() {
    local dest_dir="$1"

    [[ -z "$CFG_STALWART_URL" ]] && return 0
    [[ -z "$CFG_STALWART_ADMIN_TOKEN" ]] && return 0

    local base_url="${CFG_STALWART_URL%/}"
    local auth_header="Authorization: Bearer ${CFG_STALWART_ADMIN_TOKEN}"

    # Fetch all principals (paginated)
    local page=1 limit=100 total=0 fetched=0
    local all_principals="[]"

    while true; do
        local response
        response=$(curl -sfS -H "$auth_header" -H 'Accept: application/json' \
            "${base_url}/api/principal?page=${page}&limit=${limit}" 2>/dev/null) || break

        local items
        items=$(echo "$response" | jq -r '.data.items // []' 2>/dev/null)
        total=$(echo "$response" | jq -r '.data.total // 0' 2>/dev/null)

        [[ "$items" == "[]" || "$items" == "null" ]] && break

        all_principals=$(echo "$all_principals" "$items" | jq -s '.[0] + .[1]' 2>/dev/null)
        fetched=$((fetched + $(echo "$items" | jq 'length' 2>/dev/null || echo 0)))

        [[ $fetched -ge $total ]] && break
        page=$((page + 1))
    done

    if [[ "$all_principals" != "[]" ]]; then
        echo "$all_principals" > "${dest_dir}/principals.json"
        log_info "stalwart: Exported $fetched principals"
    fi
}
