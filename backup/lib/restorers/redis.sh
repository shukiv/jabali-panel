#!/usr/bin/env bash
# Restorer: Redis ACL user

restore_redis() {
    local extract_dir="$1" username="$2" user_id="$3" force="${4:-0}"
    local staging="${extract_dir}/tmp/jabali-backup/${username}/redis"

    [[ ! -d "$staging" ]] && return 0

    command -v redis-cli &>/dev/null || { log_warn "restore/redis: redis-cli not installed, skipping"; return 0; }

    local redis_user="jabali_${username}"
    local acl_rule="${staging}/acl_rule.txt"

    if [[ ! -f "$acl_rule" ]]; then
        log_info "restore/redis: No ACL rule found for $redis_user, skipping"
        return 0
    fi

    # Check if user already exists
    local existing
    existing=$(redis-cli ACL GETUSER "$redis_user" 2>/dev/null)

    if [[ -n "$existing" && "$existing" != *"ERR"* ]] && [[ "$force" -eq 0 ]]; then
        log_info "restore/redis: Redis ACL user $redis_user exists, skipping"
        return 0
    fi

    # Delete existing user if --force
    if [[ -n "$existing" && "$existing" != *"ERR"* ]] && [[ "$force" -eq 1 ]]; then
        redis-cli ACL DELUSER "$redis_user" &>/dev/null
    fi

    # Restore ACL rule — the rule file contains "user jabali_{user} ..."
    # We need to pass it to ACL SETUSER
    local rule_line
    rule_line=$(cat "$acl_rule")
    if [[ -n "$rule_line" ]]; then
        # The ACL LIST output format is "user <name> <rules>"
        # Strip the "user <name> " prefix to get just the rules
        local rules
        rules=$(echo "$rule_line" | sed "s/^user ${redis_user} //")
        if redis-cli ACL SETUSER "$redis_user" $rules &>/dev/null; then
            redis-cli ACL SAVE &>/dev/null || true
            log_info "restore/redis: Restored ACL user $redis_user"
        else
            log_warn "restore/redis: Failed to restore ACL user $redis_user"
            return 1
        fi
    fi
}
