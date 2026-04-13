#!/usr/bin/env bash
# Collector: home directory files

collect_files() {
    local username="$1" home_dir="$2" staging_dir="$3"

    if [[ ! -d "$home_dir" ]]; then
        log_warn "files: Home directory $home_dir does not exist, skipping"
        return 0
    fi

    # Return the path for restic to back up directly (no staging copy)
    echo "$home_dir"
    log_info "files: Added $home_dir to backup paths"
}

# Default exclude patterns for home directory backup
# Patterns are anchored to /home/*/ to avoid matching staging paths
files_excludes() {
    cat <<'EOF'
/home/*/.cache
/home/*/*/cache
/home/*/*/tmp
/home/*/node_modules
/home/*/*/*/node_modules
/home/*/vendor
/home/*/*/*/vendor
/home/*/.npm
/home/*/.composer/cache
/home/*/*/logs
/home/*/*.log
/home/*/*/*/*.log
EOF
}
