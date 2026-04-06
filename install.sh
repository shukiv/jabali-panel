#!/usr/bin/env bash

# ─── Remote install (piped via curl) ──────────────────
# Detect pipe before set -u, since BASH_SOURCE is unset when piped
_SELF="${BASH_SOURCE[0]:-}"
if [[ -z "$_SELF" || "$_SELF" == "bash" || "$_SELF" == "/dev/stdin" ]]; then
    set -eo pipefail
    REPO_URL="https://github.com/shukiv/jabali-backup.git"
    CLONE_DEST="/opt/jabali-backup"
    [[ $EUID -ne 0 ]] && echo "[✗] Run as root: curl ... | sudo bash" >&2 && exit 1
    command -v git &>/dev/null || { apt-get update -qq && apt-get install -y -qq git; }

    if [[ -d "$CLONE_DEST/.git" ]]; then
        git -C "$CLONE_DEST" pull --ff-only
        exec "$CLONE_DEST/install.sh" update
    else
        git clone "$REPO_URL" "$CLONE_DEST"
        exec "$CLONE_DEST/install.sh"
    fi
fi

set -euo pipefail

JABALI_BACKUP_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

INSTALL_BIN="/usr/local/bin"
INSTALL_LIB="/usr/local/lib/jabali-backup"
INSTALL_ETC="/etc/jabali-backup"

# ─── Colors & helpers ─────────────────────────────────

YELLOW='\033[1;33m'
GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

section() { echo -e "\n${YELLOW}=== $* ===${NC}\n"; }
ok()      { echo -e "[${GREEN}✓${NC}] $*"; }
info()    { echo -e "[${CYAN}i${NC}] $*"; }
warn()    { echo -e "[${YELLOW}!${NC}] $*" >&2; }
fail()    { echo -e "[${RED}✗${NC}] $*" >&2; exit 1; }

usage() {
    cat <<EOF
Usage: sudo ./install.sh [command]

Commands:
  install     Install jabali-backup CLI + panel addon (default)
  update      Update CLI + panel, preserve config & secrets
  uninstall   Remove jabali-backup CLI + panel addon

Examples:
  sudo ./install.sh              # Fresh install
  sudo ./install.sh update       # Update existing installation
  sudo ./install.sh uninstall    # Remove everything
EOF
    exit 0
}

# ─── Parse command ────────────────────────────────────

CMD="${1:-install}"
case "$CMD" in
    install|update|uninstall) ;;
    -h|--help|help) usage ;;
    *) fail "Unknown command: $CMD (use install, update, or uninstall)" ;;
esac

# ─── Pre-flight ───────────────────────────────────────

echo ""
echo -e "  jabali-backup ${CYAN}v${JABALI_BACKUP_VERSION}${NC} — $CMD"
echo ""

if [[ $EUID -ne 0 ]]; then
    fail "This must be run as root (sudo ./install.sh $CMD)"
fi

if [[ "$CMD" != "uninstall" && ! -f "$SCRIPT_DIR/bin/jabali-backup" ]]; then
    fail "Source files not found. Run from the jabali-backup project directory."
fi

# ─── Uninstall ────────────────────────────────────────

if [[ "$CMD" == "uninstall" ]]; then
    JABALI_PATH="${JABALI_PATH:-/var/www/jabali}"

    section "Stopping Services"

    if systemctl is-active --quiet jabali-backup.timer 2>/dev/null; then
        systemctl stop jabali-backup.timer
    fi
    systemctl disable jabali-backup.timer 2>/dev/null || true
    ok "Backup timer stopped and disabled"

    section "Removing Panel Addon"

    for f in \
        /etc/jabali/agent.d/jabali-backup.php \
        "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php" \
        "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php" \
        "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php" \
        "$JABALI_PATH/app/Backup/Adapters/ResticSnapshotAdapter.php" \
        "$JABALI_PATH/app/Backup/BackupServiceProvider.php" \
        "$JABALI_PATH/public/backup-download.php" \
        "$JABALI_PATH/resources/views/filament/admin/pages/backups.blade.php" \
        "$JABALI_PATH/resources/views/filament/admin/pages/partials/snapshot-browser-embed.blade.php" \
        "$JABALI_PATH/resources/views/filament/admin/pages/partials/restore-file-badges.blade.php" \
        "$JABALI_PATH/resources/views/filament/jabali/pages/backups.blade.php"
    do
        if [[ -f "$f" ]]; then
            rm "$f"
            ok "Removed ${f##*/}"
        fi
    done

    # Remove service provider registration
    if [[ -f "$JABALI_PATH/bootstrap/providers.php" ]]; then
        sed -i '/BackupServiceProvider/d' "$JABALI_PATH/bootstrap/providers.php" 2>/dev/null || true
        ok "Unregistered BackupServiceProvider"
    fi

    # Clear panel caches
    if [[ -f "$JABALI_PATH/artisan" ]]; then
        cd "$JABALI_PATH"
        php artisan view:clear 2>/dev/null || true
        php artisan filament:cache-components 2>/dev/null || true
        ok "Panel caches cleared"
        systemctl restart jabali-agent 2>/dev/null || true
        systemctl restart php8.5-fpm 2>/dev/null || systemctl restart php8.4-fpm 2>/dev/null || systemctl restart php8.3-fpm 2>/dev/null || true
        ok "Services restarted"
    fi

    section "Removing CLI"

    for f in /etc/systemd/system/jabali-backup.service /etc/systemd/system/jabali-backup.timer; do
        [[ -f "$f" ]] && rm "$f"
    done
    systemctl daemon-reload 2>/dev/null || true
    ok "Systemd units removed"

    [[ -f /usr/local/bin/jabali-backup ]] && rm /usr/local/bin/jabali-backup
    ok "Removed /usr/local/bin/jabali-backup"

    [[ -d /usr/local/lib/jabali-backup ]] && rm -rf /usr/local/lib/jabali-backup
    ok "Removed /usr/local/lib/jabali-backup/"

    [[ -f /etc/bash_completion.d/jabali-backup ]] && rm /etc/bash_completion.d/jabali-backup
    ok "Removed bash completions"

    section "Uninstall Complete"

    ok "jabali-backup removed"
    echo ""
    info "Kept (remove manually if desired):"
    echo "    /etc/jabali-backup/          (config & secrets)"
    echo "    /var/log/jabali-backup.log   (log file)"
    echo "    /var/cache/jabali-backup/    (restic cache)"
    echo ""
    exit 0
fi

# ─── Detect existing install & pull ───────────────────

IS_UPDATE=false
if [[ "$CMD" == "update" ]]; then
    if [[ ! -f "$INSTALL_BIN/jabali-backup" ]]; then
        fail "No existing installation found. Run ./install.sh install first."
    fi
    IS_UPDATE=true

    section "Pulling Latest"

    INSTALLED_VERSION=$("$INSTALL_BIN/jabali-backup" version 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' || echo "unknown")
    info "Installed version: v${INSTALLED_VERSION}"

    if [[ -d "$SCRIPT_DIR/.git" ]]; then
        git -C "$SCRIPT_DIR" pull --ff-only || fail "git pull failed. Resolve conflicts and re-run."
        JABALI_BACKUP_VERSION=$(grep -oP 'JABALI_BACKUP_VERSION="\K[^"]+' "$SCRIPT_DIR/bin/jabali-backup" || echo "$JABALI_BACKUP_VERSION")
        ok "Source updated to v${JABALI_BACKUP_VERSION}"
    else
        warn "Not a git checkout — skipping pull"
    fi
fi

# ─── Dependencies ─────────────────────────────────────

section "Checking Dependencies"

DEPS_TO_INSTALL=()

check_or_queue() {
    local cmd="$1" pkg="$2"
    if command -v "$cmd" &>/dev/null; then
        ok "$cmd $(command -v "$cmd")"
    else
        info "$cmd not found — will install $pkg"
        DEPS_TO_INSTALL+=("$pkg")
    fi
}

check_or_queue restic       restic
check_or_queue mysql        mysql-client
check_or_queue jq           jq
check_or_queue tar          tar
check_or_queue gzip         gzip

if [[ ${#DEPS_TO_INSTALL[@]} -gt 0 ]]; then
    echo ""
    info "Installing missing: ${DEPS_TO_INSTALL[*]}"
    apt-get update -qq
    apt-get install -y -qq "${DEPS_TO_INSTALL[@]}" || fail "Failed to install: ${DEPS_TO_INSTALL[*]}"
    ok "Dependencies installed"
fi

# ─── Install CLI ──────────────────────────────────────

section "Installing CLI"

mkdir -p "$INSTALL_BIN" \
         "$INSTALL_LIB/collectors" \
         "$INSTALL_LIB/restorers" \
         "$INSTALL_ETC"

cp "$SCRIPT_DIR/bin/jabali-backup" "$INSTALL_BIN/jabali-backup"
chmod 755 "$INSTALL_BIN/jabali-backup"
ok "jabali-backup -> $INSTALL_BIN/jabali-backup"

cp "$SCRIPT_DIR"/lib/*.sh  "$INSTALL_LIB/"
cp "$SCRIPT_DIR"/lib/*.php "$INSTALL_LIB/"
cp "$SCRIPT_DIR"/lib/collectors/*.sh "$INSTALL_LIB/collectors/"
cp "$SCRIPT_DIR"/lib/restorers/*.sh  "$INSTALL_LIB/restorers/"
ok "Libraries -> $INSTALL_LIB/"

# ─── Configuration & secrets ──────────────────────────

JABALI_PATH="${JABALI_PATH:-/var/www/jabali}"
JABALI_ENV="$JABALI_PATH/.env"

section "Configuring Secrets"

# Extract secrets from Jabali .env
if [[ -f "$JABALI_ENV" ]]; then
    # DB password
    if [[ ! -f "$INSTALL_ETC/db-password" ]]; then
        DB_PASS=$(grep -oP '^DB_PASSWORD=\K.*' "$JABALI_ENV" 2>/dev/null || true)
        if [[ -n "$DB_PASS" ]]; then
            echo "$DB_PASS" > "$INSTALL_ETC/db-password"
            chmod 600 "$INSTALL_ETC/db-password"
            ok "DB password extracted from Jabali .env"
        else
            warn "DB_PASSWORD not found in $JABALI_ENV — set manually"
        fi
    else
        ok "DB password exists (preserved)"
    fi

    # APP_KEY
    if [[ ! -f "$INSTALL_ETC/app-key" ]]; then
        APP_KEY=$(grep -oP '^APP_KEY=\K.*' "$JABALI_ENV" 2>/dev/null || true)
        if [[ -n "$APP_KEY" ]]; then
            echo "$APP_KEY" > "$INSTALL_ETC/app-key"
            chmod 600 "$INSTALL_ETC/app-key"
            ok "APP_KEY extracted from Jabali .env"
        else
            warn "APP_KEY not found in $JABALI_ENV — set manually"
        fi
    else
        ok "APP_KEY exists (preserved)"
    fi

    # DB host/name/user from .env
    DB_HOST=$(grep -oP '^DB_HOST=\K.*' "$JABALI_ENV" 2>/dev/null || echo "localhost")
    DB_NAME=$(grep -oP '^DB_DATABASE=\K.*' "$JABALI_ENV" 2>/dev/null || echo "jabali")
    DB_USER=$(grep -oP '^DB_USERNAME=\K.*' "$JABALI_ENV" 2>/dev/null || echo "root")
else
    DB_HOST="localhost"
    DB_NAME="jabali"
    DB_USER="root"
    warn "Jabali .env not found at $JABALI_ENV — secrets must be set manually"
fi

# Restic password — generate if missing
if [[ ! -f "$INSTALL_ETC/restic-password" ]]; then
    openssl rand -base64 32 > "$INSTALL_ETC/restic-password"
    chmod 600 "$INSTALL_ETC/restic-password"
    ok "Restic password generated"
else
    ok "Restic password exists (preserved)"
fi

section "Configuration"

if [[ -f "$INSTALL_ETC/config.conf" ]]; then
    ok "Config exists at $INSTALL_ETC/config.conf (preserved)"
    if [[ "$IS_UPDATE" == true ]]; then
        cp "$SCRIPT_DIR/etc/config.conf.example" "$INSTALL_ETC/config.conf.example"
        ok "Updated config.conf.example for reference"
    fi
else
    # Generate config from template with auto-detected values
    sed -e "s|^db_host=.*|db_host=${DB_HOST}|" \
        -e "s|^db_name=.*|db_name=${DB_NAME}|" \
        -e "s|^db_user=.*|db_user=${DB_USER}|" \
        -e "s|^jabali_path=.*|jabali_path=${JABALI_PATH}|" \
        "$SCRIPT_DIR/etc/config.conf.example" > "$INSTALL_ETC/config.conf"
    chmod 640 "$INSTALL_ETC/config.conf"
    ok "Config generated with auto-detected values"
fi

# ─── Bash completions ────────────────────────────────

section "Setting Up Bash Completions"

COMP_DIR="/etc/bash_completion.d"
mkdir -p "$COMP_DIR"

cat > "$COMP_DIR/jabali-backup" << 'COMP'
_jabali_backup() {
    local cur prev commands
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    commands="run restore ls list init check forget destination schedule config doctor version"

    case "$prev" in
        jabali-backup)
            COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
            return 0
            ;;
        run|restore)
            local users
            users=$(ls /home/ 2>/dev/null | tr '\n' ' ')
            COMPREPLY=( $(compgen -W "$users" -- "$cur") )
            return 0
            ;;
        list)
            COMPREPLY=( $(compgen -W "accounts snapshots domains" -- "$cur") )
            return 0
            ;;
        --only|--exclude)
            COMPREPLY=( $(compgen -W "files mysql postgres dns email ssl nginx php wordpress cron metadata" -- "$cur") )
            return 0
            ;;
        --snapshot)
            COMPREPLY=( $(compgen -W "latest" -- "$cur") )
            return 0
            ;;
    esac

    if [[ "$cur" == -* ]]; then
        local opts="--only= --exclude= --snapshot= --force --dry-run --parallel= --target= --file= --destination="
        COMPREPLY=( $(compgen -W "$opts" -- "$cur") )
        return 0
    fi
}
complete -F _jabali_backup jabali-backup
COMP

ok "Installed $COMP_DIR/jabali-backup"

# ─── Systemd timer ────────────────────────────────────

section "Setting Up Backup Timer"

cat > /etc/systemd/system/jabali-backup.service << 'UNIT'
[Unit]
Description=Jabali Backup - per-account backup
After=network-online.target mysql.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jabali-backup run
Nice=10
IOSchedulingClass=idle
TimeoutStartSec=3h
UNIT

cat > /etc/systemd/system/jabali-backup.timer << 'TIMER'
[Unit]
Description=Jabali Backup - daily at 02:00

[Timer]
OnCalendar=*-*-* 02:00:00
RandomizedDelaySec=900
Persistent=true

[Install]
WantedBy=timers.target
TIMER

systemctl daemon-reload
ok "Created jabali-backup.service + jabali-backup.timer"
if [[ "$IS_UPDATE" == false ]]; then
    info "Enable with: systemctl enable --now jabali-backup.timer"
fi

# ─── Directories & permissions ────────────────────────

section "Setting Up Directories"

mkdir -p /var/log
touch /var/log/jabali-backup.log
chmod 640 /var/log/jabali-backup.log
ok "Log file: /var/log/jabali-backup.log"

mkdir -p /var/cache/jabali-backup/restic
ok "Cache dir: /var/cache/jabali-backup/restic/"

# ─── Panel integration ────────────────────────────────

PANEL_DIR="${SCRIPT_DIR}/panel"

if [[ -f "$JABALI_PATH/artisan" && -d "$PANEL_DIR" ]]; then
    section "Installing Panel Addon"

    info "Jabali path: $JABALI_PATH"

    # Agent routes
    mkdir -p /etc/jabali/agent.d
    cp "$PANEL_DIR/agent/jabali-backup.php" /etc/jabali/agent.d/jabali-backup.php
    chmod 644 /etc/jabali/agent.d/jabali-backup.php
    ok "Agent routes -> /etc/jabali/agent.d/jabali-backup.php"

    # Admin Filament page
    mkdir -p "$JABALI_PATH/app/Filament/Admin/Pages"
    cp "$PANEL_DIR/filament/pages/Backups.php" "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php"
    ok "Admin page -> Backups.php"

    # Admin Blade views
    mkdir -p "$JABALI_PATH/resources/views/filament/admin/pages/partials"
    cp "$PANEL_DIR/views/backups.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/backups.blade.php"
    cp "$PANEL_DIR/views/partials/snapshot-browser-embed.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/partials/snapshot-browser-embed.blade.php"
    cp "$PANEL_DIR/views/partials/restore-file-badges.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/partials/restore-file-badges.blade.php"
    ok "Admin views -> backups.blade.php + partials"

    # User Filament page
    mkdir -p "$JABALI_PATH/app/Filament/Jabali/Pages"
    cp "$PANEL_DIR/filament/pages/UserBackups.php" "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php"
    ok "User page -> UserBackups.php"

    # User Blade view
    mkdir -p "$JABALI_PATH/resources/views/filament/jabali/pages"
    cp "$PANEL_DIR/views/user/backups.blade.php" "$JABALI_PATH/resources/views/filament/jabali/pages/backups.blade.php"
    ok "User view -> backups.blade.php"

    # Backup addon classes
    mkdir -p "$JABALI_PATH/app/Backup/Adapters"
    cp "$PANEL_DIR/backup/Adapters/ResticSnapshotAdapter.php" "$JABALI_PATH/app/Backup/Adapters/ResticSnapshotAdapter.php"
    cp "$PANEL_DIR/backup/BackupServiceProvider.php" "$JABALI_PATH/app/Backup/BackupServiceProvider.php"
    ok "Backup classes -> ResticSnapshotAdapter, BackupServiceProvider"

    # Snapshot browser page
    cp "$PANEL_DIR/filament/pages/SnapshotBrowser.php" "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php"
    ok "Snapshot browser -> SnapshotBrowser.php"

    # Download endpoint
    cp "$PANEL_DIR/public/backup-download.php" "$JABALI_PATH/public/backup-download.php"
    ok "Download endpoint -> backup-download.php"

    # Register service provider
    if ! grep -q 'BackupServiceProvider' "$JABALI_PATH/bootstrap/providers.php" 2>/dev/null; then
        sed -i '/^];$/i\    App\\Backup\\BackupServiceProvider::class,' "$JABALI_PATH/bootstrap/providers.php"
        ok "Registered BackupServiceProvider"
    else
        ok "BackupServiceProvider already registered"
    fi

    # Clear caches & restart
    cd "$JABALI_PATH"
    php artisan view:clear 2>/dev/null || true
    php artisan filament:cache-components 2>/dev/null || true
    ok "Caches cleared"

    systemctl restart jabali-agent 2>/dev/null || true
    systemctl restart php8.5-fpm 2>/dev/null || systemctl restart php8.4-fpm 2>/dev/null || systemctl restart php8.3-fpm 2>/dev/null || true
    ok "Services restarted (jabali-agent, php-fpm)"
else
    if [[ ! -f "$JABALI_PATH/artisan" ]]; then
        section "Panel Addon"
        info "Jabali panel not found at $JABALI_PATH — skipping panel install"
        info "Set JABALI_PATH and re-run, or run: sudo ./install-panel.sh"
    fi
fi

# ─── Enable timer ─────────────────────────────────────

section "Enabling Backup Schedule"

systemctl enable jabali-backup.timer 2>/dev/null || true
systemctl start jabali-backup.timer 2>/dev/null || true
ok "Daily backup timer enabled (02:00 with 15min jitter)"

# ─── Done ─────────────────────────────────────────────

if [[ "$IS_UPDATE" == true ]]; then
    section "Update Complete"
    ok "jabali-backup updated to v${JABALI_BACKUP_VERSION}"
    ok "Config preserved at $INSTALL_ETC/config.conf"
    ok "Secrets preserved"
    echo ""
    info "Verify: jabali-backup doctor"
    echo ""
else
    section "Installation Complete"
    ok "jabali-backup v${JABALI_BACKUP_VERSION} installed"
    echo ""
    info "Add a backup destination in the panel: Backups > Destination > Add Destination"
    echo ""
fi
