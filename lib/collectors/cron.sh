#!/usr/bin/env bash
# Collector: Cron jobs (Jabali DB + system crontab)

collect_cron() {
    local user_id="$1" username="$2" staging_dir="$3"
    local cron_dir="${staging_dir}/cron"

    # Jabali cron jobs from DB
    local jobs
    jobs=$(discover_cron_jobs "$user_id")

    if [[ -n "$jobs" ]]; then
        mkdir -p "$cron_dir"
        echo "[" > "${cron_dir}/jobs.json"
        local first=1
        while IFS=$'\t' read -r job_id name schedule command job_type is_active; do
            [[ -z "$job_id" ]] && continue
            [[ "$first" -eq 0 ]] && echo "," >> "${cron_dir}/jobs.json"
            first=0
            printf '  {"id":%s,"name":"%s","schedule":"%s","command":"%s","type":"%s","is_active":%s}' \
                "$job_id" "${name//\"/\\\"}" "$schedule" "${command//\"/\\\"}" "$job_type" "${is_active:-1}" >> "${cron_dir}/jobs.json"
        done <<< "$jobs"
        echo -e "\n]" >> "${cron_dir}/jobs.json"
        log_info "cron: Exported Jabali cron jobs"
    fi

    # System crontab
    local sys_crontab
    sys_crontab=$(crontab -u "$username" -l 2>/dev/null)
    if [[ -n "$sys_crontab" ]]; then
        mkdir -p "$cron_dir"
        echo "$sys_crontab" > "${cron_dir}/crontab"
        log_info "cron: Exported system crontab for $username"
    fi
}
