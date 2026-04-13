#!/usr/bin/env bash
# Restorer: Stalwart mail server account import via stalwart-cli
#
# Imports JMAP account data exported by the stalwart collector.
# Uses `stalwart-cli import account` for JMAP data, and the REST API
# for principal creation/updates.
#
# Force behavior:
#   --force applies to principal recreation (update existing principal).
#   JMAP import via stalwart-cli is additive — it doesn't delete existing
#   emails, so --force is not needed for message import.

restore_stalwart() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/stalwart"

    [[ ! -d "$staging" ]] && return 0
    [[ "$CFG_STALWART_ENABLED" != "true" ]] && { log_warn "restore/stalwart: Stalwart backup not enabled in config"; return 0; }

    # Restore each account
    for account_dir in "${staging}"/*/; do
        [[ ! -d "$account_dir" ]] && continue
        local account
        account=$(basename "$account_dir")
        [[ -z "$account" || "$account" == "principals.json" ]] && continue

        # Ensure the principal exists in Stalwart
        _stalwart_ensure_principal "$account" "$account_dir" "$force"

        # Import JMAP data via stalwart-cli if export was done via CLI
        if _stalwart_has_cli_export "$account_dir"; then
            _stalwart_import_cli "$account" "$account_dir"
        fi

        log_info "restore/stalwart: Restored account $account"
    done

    log_info "restore/stalwart: Done"
}

# Check if the export directory contains CLI-exported JMAP data
# stalwart-cli export produces JSON files (mailboxes, emails, sieve, etc.)
_stalwart_has_cli_export() {
    local dir="$1"
    # Look for the specific files stalwart-cli export creates
    [[ -f "${dir}/mailboxes.json" ]] || [[ -f "${dir}/emails.json" ]] || \
        [[ -f "${dir}/sieve.json" ]] || [[ -f "${dir}/identities.json" ]]
}

# Import via stalwart-cli
_stalwart_import_cli() {
    local account="$1" source_dir="$2"

    command -v stalwart-cli &>/dev/null || {
        log_warn "restore/stalwart: stalwart-cli not found, cannot import JMAP data for $account"
        return 1
    }

    local cli_args=()
    cli_args+=(-u "$CFG_STALWART_URL")
    if [[ -n "$CFG_STALWART_ADMIN_TOKEN" ]]; then
        cli_args+=(-c "$CFG_STALWART_ADMIN_TOKEN")
    fi

    # stalwart-cli import account <ACCOUNT> <PATH>
    # Imports the same structure produced by export account
    stalwart-cli "${cli_args[@]}" import account "$account" "$source_dir" 2>/dev/null || {
        log_warn "restore/stalwart: CLI import failed for $account"
        return 1
    }

    log_info "restore/stalwart: Imported JMAP data for $account via CLI"
}

# Ensure principal exists, create if needed
_stalwart_ensure_principal() {
    local account="$1" account_dir="$2" force="$3"

    [[ -z "$CFG_STALWART_URL" || -z "$CFG_STALWART_ADMIN_TOKEN" ]] && return 0

    local base_url="${CFG_STALWART_URL%/}"
    local auth_header="Authorization: Bearer ${CFG_STALWART_ADMIN_TOKEN}"

    # Check if principal already exists
    local existing
    existing=$(curl -sfS -H "$auth_header" -H 'Accept: application/json' \
        "${base_url}/api/principal/${account}" 2>/dev/null)
    local existing_data
    existing_data=$(echo "$existing" | jq -r '.data.id // empty' 2>/dev/null)

    if [[ -n "$existing_data" ]] && [[ "$force" -ne 1 ]]; then
        log_info "restore/stalwart: Principal $account exists, skipping"
        return 0
    fi

    # Read backed-up principal data
    local principal_file="${account_dir}/principal.json"
    [[ ! -f "$principal_file" ]] && return 0

    local principal_data
    principal_data=$(cat "$principal_file")

    if [[ -n "$existing_data" ]] && [[ "$force" -eq 1 ]]; then
        # Update existing principal
        local updates="[]"
        local desc
        desc=$(echo "$principal_data" | jq -r '.description // empty' 2>/dev/null)
        if [[ -n "$desc" ]]; then
            updates=$(echo "$updates" | jq --arg v "$desc" '. + [{"action":"set","field":"description","value":$v}]')
        fi
        local quota
        quota=$(echo "$principal_data" | jq -r '.quota // empty' 2>/dev/null)
        if [[ -n "$quota" ]] && [[ "$quota" != "0" ]]; then
            updates=$(echo "$updates" | jq --argjson v "$quota" '. + [{"action":"set","field":"quota","value":$v}]')
        fi

        if [[ "$updates" != "[]" ]]; then
            curl -sfS -X PATCH -H "$auth_header" -H 'Content-Type: application/json' \
                -d "$updates" "${base_url}/api/principal/${account}" 2>/dev/null || true
            log_info "restore/stalwart: Updated principal $account"
        fi
    elif [[ -z "$existing_data" ]]; then
        # Create new principal
        local create_payload
        create_payload=$(echo "$principal_data" | jq '{
            type: (.type // "individual"),
            name: (.name // empty),
            description: (.description // ""),
            quota: (.quota // 0),
            emails: (if .emails | type == "array" then .emails else [.emails] end),
            roles: (.roles // ["user"]),
            memberOf: (.memberOf // []),
            secrets: [],
            enabledPermissions: (.enabledPermissions // []),
            disabledPermissions: (.disabledPermissions // [])
        }' 2>/dev/null)

        if [[ -n "$create_payload" ]]; then
            curl -sfS -X POST -H "$auth_header" -H 'Content-Type: application/json' \
                -d "$create_payload" "${base_url}/api/principal" 2>/dev/null || {
                log_warn "restore/stalwart: Failed to create principal $account"
                return 1
            }
            log_info "restore/stalwart: Created principal $account (password must be reset)"
        fi
    fi
}
