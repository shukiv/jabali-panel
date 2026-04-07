#!/usr/bin/env bash
# Restic wrapper with multi-backend support

# Extra restic options (populated by destination setup, e.g. -o sftp.command=...)
RESTIC_EXTRA_OPTS=()

# Set up restic environment based on config
restic_env() {
    RESTIC_EXTRA_OPTS=()
    unset RESTIC_PASSWORD_FILE 2>/dev/null || true
    export RESTIC_CACHE_DIR="$CFG_RESTIC_CACHE"

    # If a destination JSON entry is loaded, use its credentials
    if [[ -n "${_DEST_JSON:-}" ]]; then
        _restic_env_from_destination
        return
    fi

    # Legacy config: set password file
    export RESTIC_PASSWORD_FILE="$CFG_RESTIC_PASSWORD_FILE"

    # Legacy: read credentials from config.conf
    case "$CFG_REPO_TYPE" in
        s3)
            local key_file secret_file
            key_file="$(cfg_get repository aws_access_key_id_file)"
            secret_file="$(cfg_get repository aws_secret_access_key_file)"
            [[ -f "$key_file" ]] && export AWS_ACCESS_KEY_ID="$(cat "$key_file")"
            [[ -f "$secret_file" ]] && export AWS_SECRET_ACCESS_KEY="$(cat "$secret_file")"
            export RESTIC_REPOSITORY="s3:${CFG_REPO_PATH#s3:}"
            ;;
        b2)
            local id_file key_file
            id_file="$(cfg_get repository b2_account_id_file)"
            key_file="$(cfg_get repository b2_account_key_file)"
            [[ -f "$id_file" ]] && export B2_ACCOUNT_ID="$(cat "$id_file")"
            [[ -f "$key_file" ]] && export B2_ACCOUNT_KEY="$(cat "$key_file")"
            export RESTIC_REPOSITORY="b2:${CFG_REPO_PATH#b2:}"
            ;;
        sftp|local|rest|rclone|gcs|azure)
            export RESTIC_REPOSITORY="$CFG_REPO_PATH"
            ;;
        *)
            log_error "Unknown repository type: $CFG_REPO_TYPE"
            return 1
            ;;
    esac
}

# Set up restic env from a destination JSON entry
_restic_env_from_destination() {
    local type="$CFG_REPO_TYPE"
    export RESTIC_REPOSITORY="$CFG_REPO_PATH"

    # Handle repo password — insecure_no_password takes priority over password file
    local insecure
    insecure=$(echo "$_DEST_JSON" | jq -r '.credentials.insecure_no_password // false')
    if [[ "$insecure" == "true" ]]; then
        RESTIC_EXTRA_OPTS+=(--insecure-no-password)
    else
        local pwd_file
        pwd_file=$(echo "$_DEST_JSON" | jq -r '.credentials.restic_password_file // empty')
        if [[ -n "$pwd_file" ]] && [[ -f "$pwd_file" ]] && [[ -s "$pwd_file" ]]; then
            export RESTIC_PASSWORD_FILE="$pwd_file"
        fi
    fi

    # Extract user@host from sftp URI for sftp.command
    local sftp_userhost=""
    if [[ "$type" == "sftp" ]]; then
        sftp_userhost=$(echo "$RESTIC_REPOSITORY" | sed 's|^sftp:||; s|:.*||')
    fi

    case "$type" in
        sftp)
            local ssh_key_file ssh_pwd_file
            ssh_key_file=$(echo "$_DEST_JSON" | jq -r '.credentials.ssh_key_file // empty')
            ssh_pwd_file=$(echo "$_DEST_JSON" | jq -r '.credentials.ssh_password_file // empty')
            if [[ -n "$ssh_key_file" ]] && [[ -f "$ssh_key_file" ]]; then
                RESTIC_EXTRA_OPTS+=(-o "sftp.command=ssh -i ${ssh_key_file} -o StrictHostKeyChecking=accept-new ${sftp_userhost} -s sftp")
            elif [[ -n "$ssh_pwd_file" ]] && [[ -f "$ssh_pwd_file" ]]; then
                export SSHPASS="$(cat "$ssh_pwd_file")"
                RESTIC_EXTRA_OPTS+=(-o "sftp.command=sshpass -e ssh -o StrictHostKeyChecking=accept-new ${sftp_userhost} -s sftp")
            fi
            ;;
        s3)
            local key_file secret_file
            key_file=$(echo "$_DEST_JSON" | jq -r '.credentials.access_key_id_file // empty')
            secret_file=$(echo "$_DEST_JSON" | jq -r '.credentials.secret_access_key_file // empty')
            [[ -f "$key_file" ]] && export AWS_ACCESS_KEY_ID="$(cat "$key_file")"
            [[ -f "$secret_file" ]] && export AWS_SECRET_ACCESS_KEY="$(cat "$secret_file")"
            ;;
        b2)
            local id_file key_file
            id_file=$(echo "$_DEST_JSON" | jq -r '.credentials.account_id_file // empty')
            key_file=$(echo "$_DEST_JSON" | jq -r '.credentials.account_key_file // empty')
            [[ -f "$id_file" ]] && export B2_ACCOUNT_ID="$(cat "$id_file")"
            [[ -f "$key_file" ]] && export B2_ACCOUNT_KEY="$(cat "$key_file")"
            ;;
        azure)
            local name key_file
            name=$(echo "$_DEST_JSON" | jq -r '.credentials.account_name // empty')
            key_file=$(echo "$_DEST_JSON" | jq -r '.credentials.account_key_file // empty')
            [[ -n "$name" ]] && export AZURE_ACCOUNT_NAME="$name"
            [[ -f "$key_file" ]] && export AZURE_ACCOUNT_KEY="$(cat "$key_file")"
            ;;
        gcs)
            local cred_file
            cred_file=$(echo "$_DEST_JSON" | jq -r '.credentials.application_credentials_file // empty')
            [[ -f "$cred_file" ]] && export GOOGLE_APPLICATION_CREDENTIALS="$cred_file"
            ;;
    esac
}

restic_init() {
    restic_env || return 1
    log_info "Initializing restic repository: $RESTIC_REPOSITORY"
    restic init "${RESTIC_EXTRA_OPTS[@]}" 2>&1
}

restic_backup() {
    local -a paths=()
    local -a tags=()
    local -a excludes=()

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --path)   paths+=("$2"); shift 2 ;;
            --tag)    tags+=("$2"); shift 2 ;;
            --exclude) excludes+=("$2"); shift 2 ;;
            *) paths+=("$1"); shift ;;
        esac
    done

    restic_env || return 1

    local -a cmd=(restic backup --verbose "${RESTIC_EXTRA_OPTS[@]}")
    for t in "${tags[@]}"; do cmd+=(--tag "$t"); done
    for e in "${excludes[@]}"; do cmd+=(--exclude "$e"); done
    cmd+=("${paths[@]}")

    log_info "Running: ${cmd[*]}"
    "${cmd[@]}" 2>&1
}

restic_snapshots() {
    restic_env || return 1
    local -a cmd=(restic snapshots --json "${RESTIC_EXTRA_OPTS[@]}")
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --tag) cmd+=(--tag "$2"); shift 2 ;;
            *) shift ;;
        esac
    done
    "${cmd[@]}" 2>/dev/null
}

restic_restore() {
    local snapshot="$1" target="$2"
    shift 2
    restic_env || return 1

    local -a cmd=(restic restore "$snapshot" --target "$target" "${RESTIC_EXTRA_OPTS[@]}")
    # Additional flags (--include, --exclude, etc.)
    while [[ $# -gt 0 ]]; do
        cmd+=("$1"); shift
    done

    log_info "Restoring snapshot $snapshot to $target"
    "${cmd[@]}" 2>&1
}

# List files in a snapshot, optionally filtered by path prefix
restic_ls() {
    local snapshot="$1"
    shift
    restic_env || return 1
    restic ls "$snapshot" "${RESTIC_EXTRA_OPTS[@]}" "$@" 2>/dev/null
}

# Dump a single file from a snapshot to stdout
restic_dump() {
    local snapshot="$1" filepath="$2"
    restic_env || return 1
    restic dump "$snapshot" "$filepath" "${RESTIC_EXTRA_OPTS[@]}" 2>/dev/null
}

restic_forget() {
    restic_env || return 1
    local -a cmd=(restic forget --prune "${RESTIC_EXTRA_OPTS[@]}")
    cmd+=(--keep-last "$CFG_KEEP_LAST")
    cmd+=(--keep-daily "$CFG_KEEP_DAILY")
    cmd+=(--keep-weekly "$CFG_KEEP_WEEKLY")
    cmd+=(--keep-monthly "$CFG_KEEP_MONTHLY")
    log_info "Applying retention policy"
    "${cmd[@]}" 2>&1
}

restic_check() {
    restic_env || return 1
    log_info "Checking repository integrity"
    restic check "${RESTIC_EXTRA_OPTS[@]}" 2>&1
}
