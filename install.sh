#!/usr/bin/env bash
set -euo pipefail

JABALI_BACKUP_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

INSTALL_BIN="/usr/local/bin"
INSTALL_LIB="/usr/local/lib/jabali-backup"
INSTALL_ETC="/etc/jabali-backup"

# ─── Helpers ──────────────────────────────────────────

info()  { echo "  [+] $*"; }
warn()  { echo "  [!] $*" >&2; }
fatal() { echo "  [x] $*" >&2; exit 1; }

check_dep() {
    local cmd="$1" pkg="${2:-$1}"
    if command -v "$cmd" &>/dev/null; then
        info "$cmd $(command -v "$cmd")"
        return 0
    else
        warn "$cmd not found — install $pkg"
        return 1
    fi
}

# ─── Pre-flight ───────────────────────────────────────

echo ""
echo "jabali-backup v${JABALI_BACKUP_VERSION} installer"
echo "========================================"
echo ""

if [[ $EUID -ne 0 ]]; then
    fatal "This installer must be run as root (sudo ./install.sh)"
fi

if [[ ! -f "$SCRIPT_DIR/bin/jabali-backup" ]]; then
    fatal "Source files not found. Run from the jabali-backup project directory."
fi

# ─── Dependency check ─────────────────────────────────

echo "[1/6] Checking dependencies..."

missing=0
check_dep restic       restic       || missing=$((missing + 1))
check_dep mysql        mysql-client || missing=$((missing + 1))
check_dep jq           jq           || missing=$((missing + 1))
check_dep tar          tar          || missing=$((missing + 1))
check_dep gzip         gzip         || missing=$((missing + 1))

if [[ $missing -gt 0 ]]; then
    echo ""
    warn "$missing missing dependency(ies). Install them and re-run."
    warn "  apt install restic mysql-client jq tar gzip"
    exit 1
fi
echo ""

# ─── Install CLI ──────────────────────────────────────

echo "[2/6] Installing CLI..."

mkdir -p "$INSTALL_BIN" \
         "$INSTALL_LIB/collectors" \
         "$INSTALL_LIB/restorers" \
         "$INSTALL_ETC"

cp "$SCRIPT_DIR/bin/jabali-backup" "$INSTALL_BIN/jabali-backup"
chmod 755 "$INSTALL_BIN/jabali-backup"
info "jabali-backup -> $INSTALL_BIN/jabali-backup"

cp "$SCRIPT_DIR"/lib/*.sh  "$INSTALL_LIB/"
cp "$SCRIPT_DIR"/lib/*.php "$INSTALL_LIB/"
cp "$SCRIPT_DIR"/lib/collectors/*.sh "$INSTALL_LIB/collectors/"
cp "$SCRIPT_DIR"/lib/restorers/*.sh  "$INSTALL_LIB/restorers/"
info "Libraries -> $INSTALL_LIB/"
echo ""

# ─── Config ───────────────────────────────────────────

echo "[3/6] Configuration..."

if [[ -f "$INSTALL_ETC/config.conf" ]]; then
    info "Config exists at $INSTALL_ETC/config.conf (not overwritten)"
else
    cp "$SCRIPT_DIR/etc/config.conf.example" "$INSTALL_ETC/config.conf"
    chmod 640 "$INSTALL_ETC/config.conf"
    info "Created $INSTALL_ETC/config.conf (edit this file)"
fi
echo ""

# ─── Bash completions ────────────────────────────────

echo "[4/6] Bash completions..."

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
            # Complete with system usernames from /home
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

info "Installed $COMP_DIR/jabali-backup"
echo ""

# ─── Systemd timer ────────────────────────────────────

echo "[5/6] Systemd timer..."

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
info "Created jabali-backup.service + jabali-backup.timer"
info "Enable with: systemctl enable --now jabali-backup.timer"
echo ""

# ─── Directories & permissions ────────────────────────

echo "[6/6] Directories & permissions..."

mkdir -p /var/log
touch /var/log/jabali-backup.log
chmod 640 /var/log/jabali-backup.log

mkdir -p /var/cache/jabali-backup/restic

info "/var/log/jabali-backup.log"
info "/var/cache/jabali-backup/restic/"
echo ""

# ─── Done ─────────────────────────────────────────────

echo "========================================"
echo "  Installed jabali-backup v${JABALI_BACKUP_VERSION}"
echo "========================================"
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
echo "Optional — install the panel UI addon:"
echo "  sudo ./install-panel.sh"
echo ""
