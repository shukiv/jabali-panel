#!/bin/bash
# Jabali Terminal — install / uninstall script
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-terminal/main/install.sh | sudo bash
#   sudo bash install.sh --uninstall
#
# IMPORTANT: This addon exposes a root shell in the browser. Do not modify
# auth flow, PTY spawn, or audit logging without a code-review note explaining
# why the change is safe. See docs/SECURITY.md.
set -euo pipefail

# Constants are referenced in functions filled during Step 4 and Step 8 of the
# construction plan. Silencing "unused" warnings while the scaffold is in place.
# shellcheck disable=SC2034
REPO_URL="https://github.com/shukiv/jabali-terminal.git"
REPO_BRANCH="main"
INSTALL_DIR="/usr/local/jabali-terminal"
CONFIG_DIR="/etc/jabali-terminal"
LOG_DIR="/var/log/jabali-terminal"
DATA_DIR="/var/lib/jabali-terminal"
RUN_DIR="/run/jabali-terminal"
SERVICE_NAME="jabali-terminal"
PANEL_DIR="/var/www/jabali"
PANEL_APP_DIR="${PANEL_DIR}/app/JabaliTerminal"

# The panel is served by FrankenPHP / Caddy on :8443 (see CLAUDE.md).
# Host nginx on this topology serves user domains + webmail and must NEVER
# be touched — injecting there by accident would put the terminal proxy
# inside someone else's vhost. install.sh therefore targets Caddy only.
PANEL_CADDYFILE="/etc/jabali/Caddyfile"

# Marker-guarded injection (SEC-REV-7). Routes are self-registered by
# JabaliTerminalPlugin::boot(), so no routes.php edit is needed either.
CADDY_BEGIN="# === JABALI-TERMINAL CADDY BEGIN ==="
CADDY_END="# === JABALI-TERMINAL CADDY END ==="

# ── Helpers ────────────────────────────────────────────────────────────────

red()    { echo -e "\033[0;31m$*\033[0m"; }
green()  { echo -e "\033[0;32m$*\033[0m"; }
yellow() { echo -e "\033[0;33m$*\033[0m"; }
cyan()   { echo -e "\033[0;36m$*\033[0m"; }
bold()   { echo -e "\033[1m$*\033[0m"; }

# Spinner — runs in background, killed by stop_spinner
_spinner_pid=""
_spinner_flag=""

start_spinner() {
    local label="$1"
    _spinner_flag=$(mktemp /tmp/.jabali-term-spinner-XXXXXX)
    (
        local frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)
        local n=${#frames[@]} i=0
        tput civis 2>/dev/null || true
        while [ -f "$_spinner_flag" ]; do
            printf "\r\033[0;36m[%s]\033[0m %s " "${frames[i % n]}" "$label" >&2
            i=$((i + 1))
            sleep 0.08
        done
    ) &
    _spinner_pid=$!
}

stop_spinner() {
    local success="${1:-true}"
    local label="$2"
    rm -f "$_spinner_flag" 2>/dev/null
    if [ -n "$_spinner_pid" ]; then
        wait "$_spinner_pid" 2>/dev/null || true
        _spinner_pid=""
    fi
    tput cnorm 2>/dev/null || true
    if [ "$success" = "true" ]; then
        printf "\r\033[0;32m[✓]\033[0m %s\n" "$label" >&2
    else
        printf "\r\033[0;31m[✗]\033[0m %s\n" "$label" >&2
    fi
}

run_with_spinner() {
    local label="$1"; shift
    start_spinner "$label"
    local log_file
    log_file=$(mktemp /tmp/jabali-term-XXXXXX.log)
    local rc=0
    "$@" > "$log_file" 2>&1 || rc=$?
    if [ $rc -eq 0 ]; then
        stop_spinner true "$label"
    else
        stop_spinner false "$label"
        yellow "    Last output:"
        tail -5 "$log_file" | sed 's/^/    /'
    fi
    rm -f "$log_file"
    return $rc
}

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        red "Error: this script must be run as root."
        exit 1
    fi
}

detect_os() {
    if [ -f /etc/os-release ]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        OS_ID="${ID:-unknown}"
        OS_VERSION="${VERSION_ID:-}"
        OS_NAME="${PRETTY_NAME:-$OS_ID}"
    else
        OS_ID="unknown"
        OS_VERSION=""
        OS_NAME="Unknown Linux"
    fi
    export OS_ID OS_VERSION OS_NAME
}

detect_pkg_manager() {
    if command -v apt-get &>/dev/null; then
        echo "apt"
    elif command -v dnf &>/dev/null; then
        echo "dnf"
    elif command -v yum &>/dev/null; then
        echo "yum"
    else
        echo "unknown"
    fi
}

pkg_install() {
    local pkg_mgr
    pkg_mgr="$(detect_pkg_manager)"
    case "$pkg_mgr" in
        apt) DEBIAN_FRONTEND=noninteractive apt-get update -qq 2>/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
        dnf) dnf install -y -q "$@" ;;
        yum) yum install -y -q "$@" ;;
        *)
            red "Error: cannot detect package manager (apt/dnf/yum). Install manually: $*"
            exit 1
            ;;
    esac
}

section()  { echo ""; yellow "=== $* ==="; }
done_ok()  { green "[✓] $*"; }

# ── Uninstall ──────────────────────────────────────────────────────────────

safe_rmdir() {
    local path="$1"
    [ -e "$path" ] || return 0
    if command -v trash &>/dev/null; then
        trash "$path" 2>/dev/null || rm -rf "$path"
    else
        rm -rf "$path"
    fi
}

# Validate + restart Caddy. The panel's Caddyfile ships with `admin off`,
# so `systemctl reload` (which uses the admin-API ExecReload) fails by
# design — a full restart is the supported path. Returns 0 if Caddy is
# happy, 1 if validation failed.
caddy_validate_and_reload() {
    local caddyfile="$1"
    if command -v frankenphp >/dev/null 2>&1; then
        if ! frankenphp validate --config "$caddyfile" >/dev/null 2>&1; then
            return 1
        fi
    fi
    if systemctl is-active --quiet jabali-panel; then
        systemctl restart jabali-panel 2>/dev/null || return 1
    fi
    return 0
}

# Inject the terminal's Caddy block between BEGIN/END markers inside the
# panel's Caddyfile. The block is inserted just before the file's last `}`
# so it lands inside the server block the panel declares. If validation
# fails, the block is stripped and we exit non-zero.
install_caddy_block() {
    local caddyfile="$PANEL_CADDYFILE"
    local snippet_path="$INSTALL_DIR/configs/caddy/jabali-terminal.caddy"

    if [ ! -f "$caddyfile" ]; then
        yellow "  Caddyfile not found at $caddyfile; add manually:"
        sed 's/^/    /' "$snippet_path"
        return 0
    fi

    # Remove any previous block first (idempotent re-install).
    if grep -qF "$CADDY_BEGIN" "$caddyfile"; then
        sed -i "/${CADDY_BEGIN}/,/${CADDY_END}/d" "$caddyfile"
    fi

    # Keep a pristine backup for revert-on-failure.
    local backup
    backup="$(mktemp)"
    cp "$caddyfile" "$backup"

    local snippet
    snippet="$(cat "$snippet_path")"

    local tmp
    tmp="$(mktemp)"
    awk -v begin="$CADDY_BEGIN" -v end="$CADDY_END" -v snip="$snippet" '
        { lines[NR] = $0 }
        END {
            last_close = 0
            for (i = NR; i >= 1; i--) {
                if (lines[i] ~ /^\s*}\s*$/) { last_close = i; break }
            }
            inserted = 0
            for (i = 1; i <= NR; i++) {
                if (i == last_close && !inserted) {
                    print "    " begin
                    n = split(snip, arr, "\n")
                    for (j = 1; j <= n; j++) {
                        # Drop full-comment and empty lines from the snippet
                        # file — they bloat the Caddyfile for zero benefit.
                        if (arr[j] ~ /^#/ || arr[j] ~ /^[[:space:]]*$/) continue
                        print "    " arr[j]
                    }
                    print "    " end
                    inserted = 1
                }
                print lines[i]
            }
            if (!inserted) {
                # No outer block — fall back to top-level insertion. Caddy
                # allows snippets outside a site block, but without a
                # server block the route has no matching host; warn below.
                print begin
                n = split(snip, arr, "\n")
                for (j = 1; j <= n; j++) {
                    if (arr[j] ~ /^#/ || arr[j] ~ /^[[:space:]]*$/) continue
                    print arr[j]
                }
                print end
            }
        }
    ' "$caddyfile" > "$tmp"
    cat "$tmp" > "$caddyfile"
    rm -f "$tmp"

    if caddy_validate_and_reload "$caddyfile"; then
        rm -f "$backup"
        done_ok "Caddy block installed + panel restarted ($caddyfile)"
    else
        red "  Caddyfile validation failed or restart errored; reverting"
        cp "$backup" "$caddyfile"
        rm -f "$backup"
        systemctl restart jabali-panel 2>/dev/null || true
        return 1
    fi
}

uninstall_caddy_block() {
    local caddyfile="$PANEL_CADDYFILE"
    [ -f "$caddyfile" ] || return 0
    if grep -qF "$CADDY_BEGIN" "$caddyfile"; then
        sed -i "/${CADDY_BEGIN}/,/${CADDY_END}/d" "$caddyfile"
        caddy_validate_and_reload "$caddyfile" || true
    fi
}

# Merge npm deps from panel/resources/package-deps.json into the panel's
# package.json. Returns 0 if no change was needed, 1 if package.json was
# modified (caller should run npm ci + npm run build).
merge_panel_npm_deps() {
    local panel_pkg="$PANEL_DIR/package.json"
    local addon_deps="$INSTALL_DIR/panel/resources/package-deps.json"
    [ -f "$panel_pkg" ] && [ -f "$addon_deps" ] || return 0
    command -v jq >/dev/null 2>&1 || { yellow "  jq missing; skipping npm dep merge"; return 0; }

    local merged
    merged="$(jq --slurpfile add "$addon_deps" \
        '.dependencies = ((.dependencies // {}) * ($add[0].dependencies // {}))' \
        "$panel_pkg")"
    if [ "$(echo "$merged" | jq -cS .)" = "$(jq -cS . < "$panel_pkg")" ]; then
        return 0
    fi
    echo "$merged" > "$panel_pkg"
    return 1
}

# Copy all panel files into /var/www/jabali. AdminPanelProvider.php is NEVER
# touched here — the parent panel uses class_exists() autodiscovery (Step 9).
install_panel_files() {
    [ -d "$PANEL_DIR/app/Filament" ] || return 0

    install -d -m 0755 \
        "$PANEL_APP_DIR" \
        "$PANEL_APP_DIR/Pages" \
        "$PANEL_APP_DIR/Http" \
        "$PANEL_APP_DIR/Http/Controllers" \
        "$PANEL_APP_DIR/views" \
        "$PANEL_DIR/resources/js" \
        "$PANEL_DIR/resources/css" \
        "$PANEL_DIR/tests/Feature"

    install -m 0644 "$INSTALL_DIR/panel/JabaliTerminalPlugin.php"          "$PANEL_APP_DIR/JabaliTerminalPlugin.php"
    install -m 0644 "$INSTALL_DIR/panel/JabaliTerminalServiceProvider.php" "$PANEL_APP_DIR/JabaliTerminalServiceProvider.php"
    install -m 0644 "$INSTALL_DIR/panel/JabaliTerminalClient.php"          "$PANEL_APP_DIR/JabaliTerminalClient.php"
    install -m 0644 "$INSTALL_DIR/panel/Pages/Terminal.php"         "$PANEL_APP_DIR/Pages/Terminal.php"
    install -m 0644 "$INSTALL_DIR/panel/Http/PreventFramingMiddleware.php" \
                    "$PANEL_APP_DIR/Http/PreventFramingMiddleware.php"
    install -m 0644 "$INSTALL_DIR/panel/Http/Controllers/TerminalSessionController.php" \
                    "$PANEL_APP_DIR/Http/Controllers/TerminalSessionController.php"
    install -m 0644 "$INSTALL_DIR/panel/views/terminal.blade.php"   "$PANEL_APP_DIR/views/terminal.blade.php"

    # Clean up legacy Sessions page + view from prior installs. Audit
    # transcripts (under $LOG_DIR/sessions/) are untouched — only the
    # defunct Filament page files are removed. Sessions now lives as a
    # tab inside Pages/Terminal.php.
    rm -f "$PANEL_APP_DIR/Pages/Sessions.php" "$PANEL_APP_DIR/views/sessions.blade.php"
    install -m 0644 "$INSTALL_DIR/panel/resources/js/jabali-terminal.js"   "$PANEL_DIR/resources/js/jabali-terminal.js"
    install -m 0644 "$INSTALL_DIR/panel/resources/css/jabali-terminal.css" "$PANEL_DIR/resources/css/jabali-terminal.css"
    install -m 0644 "$INSTALL_DIR/panel/tests/Feature/TerminalAuthTest.php" \
                    "$PANEL_DIR/tests/Feature/TerminalAuthTest.php"

    chown -R www-data:www-data "$PANEL_APP_DIR" 2>/dev/null || true
    chown www-data:www-data "$PANEL_DIR/resources/js/jabali-terminal.js" \
                            "$PANEL_DIR/resources/css/jabali-terminal.css" \
                            "$PANEL_DIR/tests/Feature/TerminalAuthTest.php" 2>/dev/null || true

    # Composer's optimized classmap caches every file it autoloaded at the
    # last `composer install` — so when we rm legacy files (e.g. the old
    # Pages/Sessions.php) the cache still includes them and Filament fatals
    # on route registration ("Failed to open stream"). Regenerate so the
    # classmap reflects disk. Run as www-data to match file ownership.
    if command -v composer >/dev/null 2>&1; then
        run_with_spinner "Regenerating composer autoload" \
            bash -c "cd '$PANEL_DIR' && sudo -u www-data composer dump-autoload -o --no-interaction --quiet"
    fi

    done_ok "Panel files installed"

    local npm_changed=0
    merge_panel_npm_deps || npm_changed=1
    if [ "$npm_changed" = "1" ] && command -v npm >/dev/null 2>&1; then
        # Use `npm install`, not `npm ci`: the deps merge mutated package.json
        # so the lockfile is out of sync; ci would refuse. install updates
        # both, then we build. --no-audit --no-fund for quiet output.
        run_with_spinner "Installing npm deps" \
            bash -c "cd '$PANEL_DIR' && npm install --no-audit --no-fund --silent"
        run_with_spinner "Building panel assets" \
            bash -c "cd '$PANEL_DIR' && npm run build --silent"
    else
        yellow "  npm deps unchanged or npm missing; asset build skipped."
        yellow "  If the terminal page fails to load JS, run:"
        yellow "    cd $PANEL_DIR && npm install && npm run build"
    fi

    # Clear caches + restart. Pattern matches jabali-security + the note from
    # the 2026-04-12 rollout.
    if [ -f "$PANEL_DIR/artisan" ]; then
        sudo -u www-data bash -c "cd '$PANEL_DIR' && php artisan optimize:clear" >/dev/null 2>&1 || true
    fi
    systemctl restart jabali-panel 2>/dev/null || true
}

uninstall_panel_files() {
    [ -d "$PANEL_APP_DIR" ] && safe_rmdir "$PANEL_APP_DIR"
    rm -f "$PANEL_DIR/resources/js/jabali-terminal.js"
    rm -f "$PANEL_DIR/resources/css/jabali-terminal.css"
    rm -f "$PANEL_DIR/tests/Feature/TerminalAuthTest.php"

    if [ -f "$PANEL_DIR/artisan" ]; then
        sudo -u www-data bash -c "cd '$PANEL_DIR' && php artisan optimize:clear" >/dev/null 2>&1 || true
    fi
    systemctl restart jabali-panel 2>/dev/null || true
}

do_uninstall() {
    require_root
    yellow "Uninstalling Jabali Terminal..."

    # Warn before removing audit transcripts — they are forensic evidence.
    if [ -d "$LOG_DIR/sessions" ] && [ -n "$(ls -A "$LOG_DIR/sessions" 2>/dev/null)" ]; then
        yellow "  Audit transcripts in $LOG_DIR/sessions/ will be deleted."
        yellow "  Press Ctrl-C within 5s to abort and preserve them."
        sleep 5 || { red "Aborted. Logs preserved."; exit 130; }
    fi

    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload 2>/dev/null || true
    done_ok "Service removed"

    rm -f "/etc/logrotate.d/${SERVICE_NAME}"
    rm -f "/usr/local/bin/${SERVICE_NAME}"
    rm -f "/etc/bash_completion.d/${SERVICE_NAME}"
    done_ok "CLI + logrotate removed"

    safe_rmdir "$INSTALL_DIR"
    safe_rmdir "$CONFIG_DIR"
    safe_rmdir "$DATA_DIR"
    safe_rmdir "$LOG_DIR"
    done_ok "Install, config, data, and log dirs removed"

    # Panel-side teardown. AdminPanelProvider.php is never touched because
    # the parent panel uses class_exists() autodiscovery (jabali-security
    # regression from 2026-04-12 informs this design).
    section "Removing Panel Integration"
    uninstall_caddy_block
    uninstall_panel_files
    done_ok "Panel integration removed"

    green "Jabali Terminal has been removed."
}

# ── Install ────────────────────────────────────────────────────────────────

do_install() {
    require_root
    echo ""
    yellow "  Jabali Terminal — Installer"
    yellow "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""

    section "Detecting System"
    detect_os
    echo "  OS: $OS_NAME (id=$OS_ID, version=${OS_VERSION:-n/a})"

    section "Installing System Dependencies"
    run_with_spinner "Installing python3 + openssl + jq" \
        pkg_install python3 python3-venv python3-pip openssl jq git ca-certificates

    if [ "$(python3 -c 'import sys; print(sys.version_info >= (3,12))' 2>/dev/null)" != "True" ]; then
        red "Error: Python 3.12+ is required. Found: $(python3 --version 2>&1)"
        red "Install Python 3.12 (e.g. deadsnakes PPA on Ubuntu 22.04) then re-run."
        exit 1
    fi
    done_ok "Python $(python3 --version 2>&1)"

    section "Creating Directories"
    install -d -m 0755 -o root -g root "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR"
    # Audit logs are private to root — SEC-REV-4.
    install -d -m 0700 -o root -g root "$LOG_DIR" "$LOG_DIR/sessions"
    # $RUN_DIR is created by systemd via RuntimeDirectory= in the unit file.
    done_ok "Dirs created"

    section "Fetching Source"
    # Dev mode: if the script is being run from inside a checkout, skip clone.
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    if [ -f "$script_dir/install.sh" ] && [ -d "$script_dir/daemon" ]; then
        if [ "$script_dir" != "$INSTALL_DIR" ]; then
            run_with_spinner "Copying source to $INSTALL_DIR" \
                bash -c "cp -a '$script_dir/.' '$INSTALL_DIR/'"
        else
            done_ok "Source already at $INSTALL_DIR (dev mode)"
        fi
    elif [ -d "$INSTALL_DIR/.git" ]; then
        run_with_spinner "Updating existing checkout" \
            bash -c "cd '$INSTALL_DIR' && git fetch --quiet && git reset --hard 'origin/$REPO_BRANCH' --quiet"
    else
        run_with_spinner "Cloning from $REPO_URL" \
            git clone --depth 1 --branch "$REPO_BRANCH" --quiet "$REPO_URL" "$INSTALL_DIR"
    fi

    section "Installing Python Daemon"
    if [ -f "$INSTALL_DIR/daemon/pyproject.toml" ]; then
        run_with_spinner "Creating venv" \
            python3 -m venv "$INSTALL_DIR/venv"
        run_with_spinner "Upgrading pip" \
            "$INSTALL_DIR/venv/bin/pip" install --upgrade --quiet pip
        run_with_spinner "Installing daemon (editable)" \
            "$INSTALL_DIR/venv/bin/pip" install --quiet -e "$INSTALL_DIR/daemon"
    else
        yellow "  daemon/pyproject.toml missing — skipping pip install (pre-Step 3 scaffold)."
    fi

    section "Generating Secrets"
    local conf="$CONFIG_DIR/jabali-terminal.conf"
    if [ -f "$conf" ] && \
       grep -qE '^hmac_secret="[0-9a-f]{64,}"' "$conf" && \
       grep -qE '^audit_hmac_secret="[0-9a-f]{64,}"' "$conf"; then
        # Re-apply perms in case a previous install left 0640 root:root.
        if getent group www-data >/dev/null 2>&1; then
            chown root:www-data "$conf" 2>/dev/null || true
        fi
        chmod 0640 "$conf" 2>/dev/null || true
        done_ok "Existing config preserved (idempotent re-install)"
    else
        local hmac_secret audit_secret
        hmac_secret="$(openssl rand -hex 32)"
        audit_secret="$(openssl rand -hex 32)"
        # Config is 0640 root:www-data so the panel (PHP-FPM / FrankenPHP
        # running as www-data) can read the session-mint HMAC secret while
        # the daemon (root) still owns the file. Falls back to root:root
        # on distros without www-data — the daemon still starts but the
        # panel client's isAvailable() will return false and the re-auth
        # modal will say "daemon unavailable" until perms are fixed.
        local conf_group="root"
        if getent group www-data >/dev/null 2>&1; then
            conf_group="www-data"
        fi
        install -m 0640 -o root -g "$conf_group" \
            "$INSTALL_DIR/configs/jabali-terminal.conf.example" "$conf"
        sed -i \
            -e "s|^hmac_secret=\"\"|hmac_secret=\"${hmac_secret}\"|" \
            -e "s|^audit_hmac_secret=\"\"|audit_hmac_secret=\"${audit_secret}\"|" \
            "$conf"
        unset hmac_secret audit_secret
        done_ok "Config written with fresh secrets at $conf (0640 root:${conf_group})"
    fi

    section "Installing systemd Unit"
    install -m 0644 "$INSTALL_DIR/configs/jabali-terminal.service" \
        "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    done_ok "systemd unit installed"

    section "Installing Logrotate"
    if [ -f "$INSTALL_DIR/configs/logrotate.d/jabali-terminal" ]; then
        install -m 0644 "$INSTALL_DIR/configs/logrotate.d/jabali-terminal" \
            "/etc/logrotate.d/${SERVICE_NAME}"
        done_ok "Logrotate config installed"
    fi

    section "Installing CLI"
    if [ -f "$INSTALL_DIR/bin/jabali-terminal" ]; then
        install -m 0755 "$INSTALL_DIR/bin/jabali-terminal" \
            "/usr/local/bin/${SERVICE_NAME}"
        done_ok "CLI symlink at /usr/local/bin/${SERVICE_NAME}"
    fi
    if [ -f "$INSTALL_DIR/completions/jabali-terminal.bash" ]; then
        install -d -m 0755 /etc/bash_completion.d
        install -m 0644 "$INSTALL_DIR/completions/jabali-terminal.bash" \
            "/etc/bash_completion.d/${SERVICE_NAME}"
    fi

    section "Starting Service"
    systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
    # Type=exec returns from systemctl start as soon as execve() succeeds, so
    # a crash-during-startup can race with the is-active check. Sleep briefly
    # first to let any immediate failure propagate, then require two
    # consecutive "active" reads with is-failed off.
    systemctl start "$SERVICE_NAME" || true
    sleep 2
    local i=0 last_ok=0
    while [ $i -lt 10 ]; do
        if systemctl is-failed --quiet "$SERVICE_NAME"; then
            last_ok=0
            break
        fi
        if systemctl is-active --quiet "$SERVICE_NAME"; then
            last_ok=$((last_ok + 1))
            if [ "$last_ok" -ge 2 ]; then
                done_ok "Service active"
                break
            fi
        else
            last_ok=0
        fi
        sleep 1; i=$((i+1))
    done
    if [ "$last_ok" -lt 2 ]; then
        red "Service failed to become active. Last journal lines:"
        journalctl -u "$SERVICE_NAME" -n 30 --no-pager || true
        exit 1
    fi

    section "Installing Panel Integration"
    if [ -d "$PANEL_DIR" ]; then
        install_panel_files
        install_caddy_block || yellow "  Caddy block install skipped (see above)"
    else
        yellow "  $PANEL_DIR not found — skipping panel integration."
        yellow "  Install jabali-panel first, then re-run this installer."
    fi

    section "Done"
    echo ""
    green "  Jabali Terminal daemon installed."
    cyan  "  Socket:    $RUN_DIR/${SERVICE_NAME}.sock"
    cyan  "  Config:    $CONFIG_DIR/jabali-terminal.conf"
    cyan  "  Audit log: $LOG_DIR/sessions/"
    cyan  "  CLI:       jabali-terminal status | logs | sessions"
    echo ""
    yellow "  Panel integration is installed separately via"
    yellow "  Jabali Panel -> Server Settings -> Addons -> Terminal."
}

# ── Dispatch ──────────────────────────────────────────────────────────────

main() {
    case "${1:-install}" in
        --uninstall|uninstall)
            do_uninstall
            ;;
        --help|-h|help)
            cat <<EOF
Jabali Terminal installer

Usage:
  sudo bash install.sh                Install (default)
  sudo bash install.sh --uninstall    Remove everything
  sudo bash install.sh --help         Show this message
EOF
            ;;
        install|"")
            do_install
            ;;
        *)
            red "Unknown argument: $1"
            exit 2
            ;;
    esac
}

main "$@"
