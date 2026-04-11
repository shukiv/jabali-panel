#!/usr/bin/env bash
# Collector: Redis ACL user

collect_redis() {
    local user_id="$1" staging_dir="$2" username="$3"

    # Check if redis-cli is available
    command -v redis-cli &>/dev/null || { log_info "redis: redis-cli not installed, skipping"; return 0; }

    local redis_user="jabali_${username}"
    local redis_dir="${staging_dir}/redis"

    # Build redis-cli auth args
    local -a redis_args=()
    local redis_pass_file="/etc/jabali-backup/redis-password"
    if [[ -f "$redis_pass_file" ]]; then
        redis_args+=(-a "$(cat "$redis_pass_file")" --no-auth-warning)
    elif [[ -f "/etc/redis/redis.conf" ]]; then
        local redis_pass
        redis_pass=$(grep -oP '^requirepass\s+\K\S+' /etc/redis/redis.conf 2>/dev/null || true)
        [[ -n "$redis_pass" ]] && redis_args+=(-a "$redis_pass" --no-auth-warning)
    fi

    # Check if the Redis ACL user exists
    local acl_info
    acl_info=$(redis-cli "${redis_args[@]}" ACL GETUSER "$redis_user" 2>/dev/null)
    if [[ -z "$acl_info" || "$acl_info" == *"ERR"* || "$acl_info" == *"NOAUTH"* ]]; then
        log_info "redis: No Redis ACL user $redis_user found (or auth failed), skipping"
        return 0
    fi

    mkdir -p "$redis_dir"

    # Export ACL user definition
    redis-cli "${redis_args[@]}" ACL GETUSER "$redis_user" > "${redis_dir}/acl_user.txt" 2>/dev/null

    # Export the ACL rule in SET format for easy restore
    local acl_list
    acl_list=$(redis-cli "${redis_args[@]}" ACL LIST 2>/dev/null | grep "^user ${redis_user} ")
    if [[ -n "$acl_list" ]]; then
        echo "$acl_list" > "${redis_dir}/acl_rule.txt"
    fi

    log_info "redis: Exported ACL user $redis_user"
}
