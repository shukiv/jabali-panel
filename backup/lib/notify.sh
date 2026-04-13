#!/usr/bin/env bash
# Notification on backup success/failure

notify_result() {
    local status="$1"  # success | failure
    local message="$2"
    local duration="$3"
    local accounts="$4"

    # Check if notification should be sent
    if [[ "$status" == "success" && "$CFG_NOTIFY_SUCCESS" != "true" ]]; then
        return
    fi
    if [[ "$status" == "failure" && "$CFG_NOTIFY_FAILURE" != "true" ]]; then
        return
    fi

    local subject="[jabali-backup] Backup ${status}: $(hostname) - $(date '+%Y-%m-%d %H:%M')"
    local body
    body="Status: ${status^^}
Hostname: $(hostname)
Date: $(date '+%Y-%m-%d %H:%M:%S')
Duration: ${duration}s
Accounts: ${accounts}

${message}"

    # Email notification
    if [[ -n "$CFG_NOTIFY_EMAIL" ]]; then
        if command -v mail &>/dev/null; then
            echo "$body" | mail -s "$subject" "$CFG_NOTIFY_EMAIL"
            log_info "Email notification sent to $CFG_NOTIFY_EMAIL"
        else
            log_warn "mail command not found, skipping email notification"
        fi
    fi

    # Webhook notification
    if [[ -n "$CFG_NOTIFY_WEBHOOK" ]]; then
        local json
        json=$(printf '{"status":"%s","hostname":"%s","date":"%s","duration":%s,"accounts":"%s","message":"%s"}' \
            "$status" "$(hostname)" "$(date -Iseconds)" "$duration" "$accounts" "$message")
        if curl -sf -X POST -H "Content-Type: application/json" -d "$json" "$CFG_NOTIFY_WEBHOOK" >/dev/null 2>&1; then
            log_info "Webhook notification sent"
        else
            log_warn "Webhook notification failed"
        fi
    fi
}
