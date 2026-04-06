#!/usr/bin/env bash
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
  install   Install jabali-backup (default)
  update    Update CLI + libraries, preserve config & secrets
  panel     Install the panel UI addon (runs install-panel.sh)

Examples:
  sudo ./install.sh            # Fresh install
  sudo ./install.sh update     # Update existing installation
  sudo ./install.sh panel      # Install panel addon
EOF
    exit 0
}

# ─── Parse command ────────────────────────────────────

CMD="${1:-install}"
case "$CMD" in
    install|update|panel) ;;
    -h|--help|help) usage ;;
    *) fail "Unknown command: $CMD (use install, update, or panel)" ;;
esac

# ─── Pre-flight ───────────────────────────────────────

echo ""
echo -e "  jabali-backup ${CYAN}v${JABALI_BACKUP_VERSION}${NC} — $CMD"
echo ""

if [[ $EUID -ne 0 ]]; then
    fail "This must be run as root (sudo ./install.sh $CMD)"
fi

if [[ ! -f "$SCRIPT_DIR/bin/jabali-backup" ]]; then
    fail "Source files not found. Run from the jabali-backup project directory."
fi

# ─── Panel addon shortcut ─────────────────────────────

if [[ "$CMD" == "panel" ]]; then
    if [[ ! -f "$SCRIPT_DIR/install-panel.sh" ]]; then
        fail "install-panel.sh not found"
    fi
    exec "$SCRIPT_DIR/install-panel.sh"
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

# ─── Configuration ────────────────────────────────────

section "Configuration"

if [[ -f "$INSTALL_ETC/config.conf" ]]; then
    ok "Config exists at $INSTALL_ETC/config.conf (preserved)"
    if [[ "$IS_UPDATE" == true ]]; then
        cp "$SCRIPT_DIR/etc/config.conf.example" "$INSTALL_ETC/config.conf.example"
        ok "Updated config.conf.example for reference"
    fi
else
    cp "$SCRIPT_DIR/etc/config.conf.example" "$INSTALL_ETC/config.conf"
    chmod 640 "$INSTALL_ETC/config.conf"
    ok "Created $INSTALL_ETC/config.conf"
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

# ─── Done ─────────────────────────────────────────────

if [[ "$IS_UPDATE" == true ]]; then
    section "Update Complete"
    ok "jabali-backup updated to v${JABALI_BACKUP_VERSION}"
    ok "Config preserved at $INSTALL_ETC/config.conf"
    ok "Secrets preserved (db-password, restic-password, app-key)"
    echo ""
    info "Verify: jabali-backup doctor"
    echo ""
else
    section "Installation Complete"
    ok "jabali-backup v${JABALI_BACKUP_VERSION} installed"
    echo ""
    echo "Next steps:"
    echo "  1. Edit /etc/jabali-backup/config.conf"
    echo "  2. Set up secrets:"
    echo "     echo 'DB_PASSWORD' > /etc/jabali-backup/db-password"
    echo "     echo 'RESTIC_PASSWORD' > /etc/jabali-backup/restic-password"
    echo "     grep APP_KEY /var/www/jabali/.env | cut -d= -f2 > /etc/jabali-backup/app-key"
    echo "     chmod 600 /etc/jabali-backup/{db-password,restic-password,app-key}"
    echo "  3. Initialize:  jabali-backup init"
    echo "  4. Verify:      jabali-backup doctor && jabali-backup config test"
    echo "  5. Enable timer: systemctl enable --now jabali-backup.timer"
    echo ""
    info "Install the panel UI addon: sudo ./install.sh panel"
    echo ""
fi
