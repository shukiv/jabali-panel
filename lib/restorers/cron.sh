#!/usr/bin/env bash
# Restorer: cron jobs (Jabali DB + system crontab)

restore_cron() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/cron"

    [[ ! -d "$staging" ]] && return 0

    # Restore Jabali cron jobs from DB
    local jobs_file="${staging}/jobs.json"
    if [[ -f "$jobs_file" ]]; then
        while IFS= read -r line; do
            local jname jschedule jcommand jtype jactive
            jname=$(echo "$line" | grep -oP '"name"\s*:\s*"\K[^"]+' || true)
            jschedule=$(echo "$line" | grep -oP '"schedule"\s*:\s*"\K[^"]+' || true)
            jcommand=$(echo "$line" | grep -oP '"command"\s*:\s*"\K[^"]+' || true)
            jtype=$(echo "$line" | grep -oP '"type"\s*:\s*"\K[^"]+' || true)
            jactive=$(echo "$line" | grep -oP '"is_active"\s*:\s*\K[0-9]+' || echo "1")

            [[ -z "$jname" ]] && continue

            local exists
            exists=$(_db_query "SELECT 1 FROM cron_jobs WHERE user_id=$user_id AND name='$(mysql_escape "$jname")' LIMIT 1")
            if [[ -z "$exists" ]] || [[ "$force" -eq 1 ]]; then
                [[ -n "$exists" ]] && _db_write "DELETE FROM cron_jobs WHERE user_id=$user_id AND name='$(mysql_escape "$jname")'"
                _db_write "INSERT INTO cron_jobs (user_id, name, schedule, command, type, is_active, created_at, updated_at) VALUES ($user_id, '$(mysql_escape "$jname")', '$(mysql_escape "$jschedule")', '$(mysql_escape "$jcommand")', '$(mysql_escape "${jtype:-command}")', $jactive, NOW(), NOW())"
                log_info "restore/cron: Restored job '$jname'"
            fi
        done < <(grep -oP '\{[^}]+\}' "$jobs_file")
    fi

    # Restore system crontab
    local crontab_file="${staging}/crontab"
    if [[ -f "$crontab_file" ]]; then
        if id "$username" &>/dev/null; then
            crontab -u "$username" "$crontab_file" 2>/dev/null && \
                log_info "restore/cron: Restored system crontab for $username"
        else
            log_warn "restore/cron: User $username doesn't exist on system, skipping crontab"
        fi
    fi

    log_info "restore/cron: Done"
}
