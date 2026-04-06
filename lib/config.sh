#!/usr/bin/env bash
# INI config file parser

JABALI_BACKUP_CONFIG="${JABALI_BACKUP_CONFIG:-/etc/jabali-backup/config.conf}"

# Read a value from the config file: cfg_get <section> <key> [default]
cfg_get() {
    local section="$1" key="$2" default="${3:-}"
    if [[ ! -f "$JABALI_BACKUP_CONFIG" ]]; then
        echo "$default"
        return
    fi
    local in_section=0 value=""
    while IFS= read -r line; do
        # Strip leading/trailing whitespace
        line="${line#"${line%%[![:space:]]*}"}"
        line="${line%"${line##*[![:space:]]}"}"
        # Skip empty lines and comments
        [[ -z "$line" || "$line" == \#* ]] && continue
        # Section header
        if [[ "$line" =~ ^\[([a-zA-Z0-9_]+)\]$ ]]; then
            [[ "${BASH_REMATCH[1]}" == "$section" ]] && in_section=1 || in_section=0
            continue
        fi
        # Key=value in target section
        if [[ "$in_section" -eq 1 ]] && [[ "$line" =~ ^([a-zA-Z0-9_]+)=(.*)$ ]]; then
            if [[ "${BASH_REMATCH[1]}" == "$key" ]]; then
                value="${BASH_REMATCH[2]}"
            fi
        fi
    done < "$JABALI_BACKUP_CONFIG"
    echo "${value:-$default}"
}

# Read a secret from a file reference
cfg_secret() {
    local section="$1" key="$2"
    local path
    path="$(cfg_get "$section" "$key")"
    if [[ -n "$path" ]] && [[ -f "$path" ]]; then
        cat "$path"
    fi
}

# Load all config into variables
cfg_load() {
    CFG_DB_HOST="$(cfg_get jabali db_host localhost)"
    CFG_DB_NAME="$(cfg_get jabali db_name jabali)"
    CFG_DB_USER="$(cfg_get jabali db_user jabali_backup)"
    CFG_DB_PASS="$(cfg_secret jabali db_password_file)"
    CFG_APP_KEY="$(cfg_secret jabali app_key_file)"
    CFG_JABALI_PATH="$(cfg_get jabali jabali_path /var/www/jabali)"

    CFG_RESTIC_PASSWORD_FILE="$(cfg_get restic password_file /etc/jabali-backup/restic-password)"
    CFG_RESTIC_CACHE="$(cfg_get restic cache_dir /var/cache/jabali-backup/restic)"

    CFG_REPO_TYPE="$(cfg_get repository type sftp)"
    CFG_REPO_PATH="$(cfg_get repository path)"

    CFG_KEEP_LAST="$(cfg_get retention keep_last 7)"
    CFG_KEEP_DAILY="$(cfg_get retention keep_daily 30)"
    CFG_KEEP_WEEKLY="$(cfg_get retention keep_weekly 12)"
    CFG_KEEP_MONTHLY="$(cfg_get retention keep_monthly 24)"

    CFG_STAGING_DIR="$(cfg_get staging dir /tmp/jabali-backup)"
    CFG_STAGING_CLEANUP="$(cfg_get staging cleanup true)"

    CFG_NOTIFY_FAILURE="$(cfg_get notifications on_failure true)"
    CFG_NOTIFY_SUCCESS="$(cfg_get notifications on_success false)"
    CFG_NOTIFY_EMAIL="$(cfg_get notifications email_to)"
    CFG_NOTIFY_WEBHOOK="$(cfg_get notifications webhook_url)"

    CFG_STALWART_DATA="$(cfg_get paths stalwart_data /opt/stalwart-mail/data)"
    CFG_PHP_FPM_POOLS="$(cfg_get paths php_fpm_pools '/etc/php/*/fpm/pool.d')"
    CFG_NGINX_SITES="$(cfg_get paths nginx_sites /etc/nginx/sites-available)"

    CFG_LOG_FILE="$(cfg_get logging file /var/log/jabali-backup.log)"
    CFG_LOG_LEVEL="$(cfg_get logging level info)"

    LOG_FILE="$CFG_LOG_FILE"
    LOG_LEVEL="$CFG_LOG_LEVEL"

    # Override with destination from destinations.json if available
    cfg_load_destination
}

# Load destination from destinations.json (overrides config.conf [repository])
# Uses global JABALI_DESTINATION if set (from --destination= flag)
cfg_load_destination() {
    local dest_file="${JABALI_BACKUP_CONFIG%/*}/destinations.json"
    [[ -f "$dest_file" ]] || return 0  # No destinations file = use config.conf
    command -v jq &>/dev/null || return 0  # No jq = use config.conf

    local dest_name="${JABALI_DESTINATION:-}"
    local dest

    if [[ -n "$dest_name" ]]; then
        # Find by name or id
        dest=$(jq -r --arg n "$dest_name" '.[] | select(.id == $n or .name == $n)' "$dest_file" 2>/dev/null)
        if [[ -z "$dest" ]]; then
            log_error "Destination not found: $dest_name"
            return 1
        fi
    else
        # Use default
        dest=$(jq -r '.[] | select(.is_default)' "$dest_file" 2>/dev/null)
        if [[ -z "$dest" ]]; then
            # Fallback to first entry
            dest=$(jq -r '.[0]' "$dest_file" 2>/dev/null)
        fi
    fi

    [[ -z "$dest" || "$dest" == "null" ]] && return 0

    # Override repo config
    CFG_REPO_TYPE=$(echo "$dest" | jq -r '.type // empty')
    CFG_REPO_PATH=$(echo "$dest" | jq -r '.path // empty')

    # Override restic password file if destination has its own
    local pwd_file
    pwd_file=$(echo "$dest" | jq -r '.credentials.restic_password_file // empty')
    if [[ -n "$pwd_file" ]] && [[ -f "$pwd_file" ]]; then
        CFG_RESTIC_PASSWORD_FILE="$pwd_file"
    fi

    # Override retention if destination has its own
    local keep
    keep=$(echo "$dest" | jq -r '.retention.keep_last // empty')
    [[ -n "$keep" ]] && CFG_KEEP_LAST="$keep"
    keep=$(echo "$dest" | jq -r '.retention.keep_daily // empty')
    [[ -n "$keep" ]] && CFG_KEEP_DAILY="$keep"
    keep=$(echo "$dest" | jq -r '.retention.keep_weekly // empty')
    [[ -n "$keep" ]] && CFG_KEEP_WEEKLY="$keep"
    keep=$(echo "$dest" | jq -r '.retention.keep_monthly // empty')
    [[ -n "$keep" ]] && CFG_KEEP_MONTHLY="$keep"

    # Export credential env vars for restic_env()
    export _DEST_JSON="$dest"
}

# Validate required config
cfg_validate() {
    local ok=0
    [[ -z "$CFG_REPO_PATH" ]] && log_error "repository.path is required" && ok=1
    [[ -z "$CFG_DB_PASS" ]] && log_error "jabali.db_password_file missing or unreadable" && ok=1
    [[ ! -f "$CFG_RESTIC_PASSWORD_FILE" ]] && log_error "restic.password_file not found: $CFG_RESTIC_PASSWORD_FILE" && ok=1
    return $ok
}
