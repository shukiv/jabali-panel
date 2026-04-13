#!/bin/bash
# Jabali Security — install / uninstall script
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-security/master/install.sh | sudo bash
#   sudo bash install.sh --uninstall
set -euo pipefail

REPO_URL="https://github.com/shukiv/jabali-security.git"
INSTALL_DIR="/usr/local/jabali-security"
CONFIG_DIR="/etc/jabali-security"
LOG_DIR="/var/log/jabali-security"
DATA_DIR="/var/lib/jabali-security"
QUARANTINE_DIR="/var/security/quarantine"
SERVICE_NAME="jabali-security"
SYSCTL_CONF="/etc/sysctl.d/99-jabali-security.conf"

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
    _spinner_flag=$(mktemp /tmp/.jabali-sec-spinner-XXXXXX)
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

# Run a command with spinner
run_with_spinner() {
    local label="$1"; shift
    start_spinner "$label"
    local log_file
    log_file=$(mktemp /tmp/jabali-sec-XXXXXX.log)
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
    # Detect distro from /etc/os-release (standard on systemd-based distros)
    if [ -f /etc/os-release ]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        OS_ID="${ID:-unknown}"
        OS_VERSION="${VERSION_ID:-}"
        OS_NAME="${PRETTY_NAME:-$OS_ID}"
    elif [ -f /etc/redhat-release ]; then
        OS_ID="rhel"
        OS_VERSION="$(grep -oE '[0-9]+' /etc/redhat-release | head -1)"
        OS_NAME="$(cat /etc/redhat-release)"
    elif [ -f /etc/debian_version ]; then
        OS_ID="debian"
        OS_VERSION="$(cat /etc/debian_version)"
        OS_NAME="Debian $OS_VERSION"
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

# ── Uninstall ──────────────────────────────────────────────────────────────

do_uninstall() {
    require_root
    yellow "Uninstalling Jabali Security..."

    # Stop and disable all managed services
    for svc in "$SERVICE_NAME" crowdsec-firewall-bouncer crowdsec clamav-freshclam; do
        systemctl stop "$svc" 2>/dev/null || true
        systemctl disable "$svc" 2>/dev/null || true
    done
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload 2>/dev/null || true

    # Remove installation
    echo "Removing $INSTALL_DIR..."
    rm -rf "$INSTALL_DIR"

    # Remove CLI symlink
    rm -f /usr/local/bin/jabali-security

    # Remove config, data, logs, quarantine
    echo "Removing config ($CONFIG_DIR)..."
    rm -rf "$CONFIG_DIR"
    echo "Removing data ($DATA_DIR)..."
    rm -rf "$DATA_DIR"
    echo "Removing logs ($LOG_DIR)..."
    rm -rf "$LOG_DIR"
    echo "Removing quarantine ($QUARANTINE_DIR)..."
    rm -rf "$QUARANTINE_DIR"

    # Remove CrowdSec bouncer and agent
    if command -v cscli &>/dev/null; then
        echo "Removing CrowdSec bouncer..."
        cscli bouncers delete jabali-fw-bouncer 2>/dev/null || true
        cscli bouncers delete jabali-security 2>/dev/null || true
        dpkg --purge crowdsec-firewall-bouncer-nftables 2>/dev/null || true
        echo "Removing CrowdSec agent..."
        systemctl stop crowdsec 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq crowdsec 2>/dev/null || true
        rm -f /etc/apt/sources.list.d/crowdsec_crowdsec.list
        rm -f /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg
    fi

    # Remove ClamAV (CLI + freshclam only — we never install the daemon)
    if command -v clamscan &>/dev/null; then
        echo "Removing ClamAV..."
        DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq clamav clamav-freshclam 2>/dev/null || true
    fi

    # Remove Jabali Panel plugin
    if [ -d "/var/www/jabali/app/JabaliSecurity" ]; then
        echo "Removing Jabali Panel plugin..."
        rm -rf "/var/www/jabali/app/JabaliSecurity"
        # AdminPanelProvider uses class_exists() guard — no sed needed
    fi

    # Remove sudoers rules
    rm -f /etc/sudoers.d/jabali-security

    # Remove service user
    if id jabali-security &>/dev/null; then
        echo "Removing service user..."
        userdel jabali-security 2>/dev/null || true
    fi

    # Remove sysctl config
    if [ -f "$SYSCTL_CONF" ]; then
        rm -f "$SYSCTL_CONF"
        sysctl --system &>/dev/null || true
    fi

    # Clean apt cache
    apt-get autoremove -y -qq 2>/dev/null || true

    green "Jabali Security has been completely removed."
}

# ── Install ────────────────────────────────────────────────────────────────

section() { echo ""; yellow "=== $* ==="; }
done_ok() { green "[✓] $*"; }

do_install() {
    require_root
    echo ""
    echo ""
    yellow "  Jabali Security — Installer"
    yellow "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo ""

    # -- Detect OS --
    section "Detecting System"
    detect_os
    echo "  OS: $OS_NAME (id=$OS_ID, version=${OS_VERSION:-n/a})"

    section "Installing System Dependencies"
    local pkg_mgr
    pkg_mgr="$(detect_pkg_manager)"

    case "$pkg_mgr" in
        apt)
            run_with_spinner "Installing core packages" \
                pkg_install git python3 python3-venv python3-pip file coreutils nftables ufw
            run_with_spinner "Installing ModSecurity (optional)" \
                pkg_install libnginx-mod-http-modsecurity || true
            # Install OWASP CRS from GitHub (apt package lags behind)
            _crs_dir="/usr/local/share/owasp-crs"
            if [ ! -d "$_crs_dir/rules" ]; then
                run_with_spinner "Installing OWASP CRS" bash -c "
                    git clone --depth 1 --quiet https://github.com/coreruleset/coreruleset.git '$_crs_dir' 2>&1 && \
                    cp '$_crs_dir/crs-setup.conf.example' '$_crs_dir/crs-setup.conf' 2>/dev/null
                " || true
            else
                green "[✓] OWASP CRS already installed"
            fi
            ;;
        dnf)
            run_with_spinner "Installing core packages" \
                pkg_install git python3 python3-pip file coreutils nftables ufw
            run_with_spinner "Installing ModSecurity (optional)" \
                pkg_install mod_security || true
            _crs_dir="/usr/local/share/owasp-crs"
            if [ ! -d "$_crs_dir/rules" ]; then
                run_with_spinner "Installing OWASP CRS" bash -c "
                    git clone --depth 1 --quiet https://github.com/coreruleset/coreruleset.git '$_crs_dir' 2>&1 && \
                    cp '$_crs_dir/crs-setup.conf.example' '$_crs_dir/crs-setup.conf' 2>/dev/null
                " || true
            fi
            ;;
        yum)
            run_with_spinner "Installing core packages" \
                pkg_install git python3 python3-pip file coreutils nftables ufw
            run_with_spinner "Installing ModSecurity (optional)" \
                pkg_install mod_security || true
            _crs_dir="/usr/local/share/owasp-crs"
            if [ ! -d "$_crs_dir/rules" ]; then
                run_with_spinner "Installing OWASP CRS" bash -c "
                    git clone --depth 1 --quiet https://github.com/coreruleset/coreruleset.git '$_crs_dir' 2>&1 && \
                    cp '$_crs_dir/crs-setup.conf.example' '$_crs_dir/crs-setup.conf' 2>/dev/null
                " || true
            fi
            ;;
        *)
            red "Error: cannot detect package manager (apt/dnf/yum)."
            exit 1
            ;;
    esac

    # -- Verify Python 3.12+ --
    if ! command -v python3 &>/dev/null; then
        red "Error: Python 3 installation failed."
        exit 1
    fi
    if [ "$(python3 -c 'import sys; print(sys.version_info >= (3,12))' 2>/dev/null)" != "True" ]; then
        red "Error: Python 3.12+ is required. Found: $(python3 --version 2>&1)"
        red "Please install Python 3.12 or later manually."
        exit 1
    fi
    done_ok "Python $(python3 --version 2>&1)"

    if ! command -v clamscan &>/dev/null; then
        # Install ClamAV CLI + definitions only — NOT clamav-daemon.
        # clamd runs ~950MB RSS permanently, unacceptable on small VPS.
        # YARA-X is the primary real-time scanner; clamscan is for manual use.
        case "$pkg_mgr" in
            apt) run_with_spinner "Installing ClamAV" pkg_install clamav clamav-freshclam
                 pkg_install geoipupdate libnginx-mod-http-js 2>/dev/null || true ;;
            dnf) run_with_spinner "Installing ClamAV" pkg_install clamav clamav-update
                 pkg_install geoipupdate 2>/dev/null || true ;;
            yum) run_with_spinner "Installing ClamAV" pkg_install clamav clamav-update
                 pkg_install geoipupdate 2>/dev/null || true ;;
        esac
    else
        green "[✓] ClamAV already installed"
    fi

    # -- CrowdSec (community threat intelligence) --
    if ! command -v cscli &>/dev/null; then
        case "$pkg_mgr" in
            apt)
                run_with_spinner "Installing CrowdSec agent" bash -c '
                    mkdir -p /etc/apt/keyrings
                    curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey \
                        | gpg --dearmor -o /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg 2>/dev/null
                    echo "deb [signed-by=/etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg] https://packagecloud.io/crowdsec/crowdsec/any/ any main" \
                        > /etc/apt/sources.list.d/crowdsec_crowdsec.list
                    apt-get update -qq 2>/dev/null
                    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq crowdsec 2>/dev/null
                '
                if command -v cscli &>/dev/null; then
                    systemctl start crowdsec 2>/dev/null
                    sleep 2
                fi

                # The bouncer install is tricky because:
                # 1. install.sh runs via curl|bash so stdin is EOF
                # 2. dpkg conffile prompts read from stdin → EOF → error
                # 3. A broken bouncer from a previous attempt blocks ALL apt ops
                #
                # Strategy: purge any old state, install clean (no conffile
                # conflict), let postinst fail (no API key yet), then patch
                # config and finalize.
                if command -v cscli &>/dev/null; then
                    # Purge broken/old bouncer state including conffiles
                    dpkg --purge --force-remove-reinstreq \
                        crowdsec-firewall-bouncer-nftables 2>/dev/null || true

                    # Verify CrowdSec LAPI is running
                    if ! systemctl is-active --quiet crowdsec 2>/dev/null; then
                        systemctl restart crowdsec 2>/dev/null
                        sleep 3
                    fi

                    # Install fresh — no old conffile means no conffile prompt.
                    # Postinst will fail (no valid API key yet) — that's expected.
                    run_with_spinner "Installing firewall bouncer" bash -c '
                        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
                            crowdsec-firewall-bouncer-nftables 2>&1
                    ' || true

                    # Generate API key and patch the config the package installed
                    cscli bouncers delete jabali-fw-bouncer 2>/dev/null || true
                    _bouncer_key=$(cscli bouncers add jabali-fw-bouncer -o raw 2>/dev/null || echo "")
                    _bouncer_cfg="/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml"
                    if [ -n "$_bouncer_key" ] && [ -f "$_bouncer_cfg" ]; then
                        sed -i "s|^api_key:.*|api_key: ${_bouncer_key}|" "$_bouncer_cfg"
                        sed -i "s|^api_url:.*|api_url: http://127.0.0.1:8080/|" "$_bouncer_cfg"
                    fi

                    # Finalize: reconfigure the package (postinst restarts the
                    # service, which now has a valid API key and correct LAPI URL).
                    # --configure only runs postinst, no conffile prompts.
                    dpkg --configure crowdsec-firewall-bouncer-nftables 2>/dev/null || {
                        dpkg --remove --force-remove-reinstreq \
                            crowdsec-firewall-bouncer-nftables 2>/dev/null || true
                        dpkg --configure -a 2>/dev/null || true
                        yellow "  Firewall bouncer install failed (non-critical)."
                    }
                fi
                ;;
        esac
    else
        green "[✓] CrowdSec already installed"
    fi

    # Install hosting-relevant CrowdSec collections
    if command -v cscli &>/dev/null; then
        for col in linux sshd nginx base-http-scenarios; do
            cscli collections install "crowdsecurity/$col" >/dev/null 2>&1 || true
        done
        done_ok "CrowdSec collections installed"
    fi

    local tmp_dir
    tmp_dir="$(mktemp -d)"
    run_with_spinner "Downloading Jabali Security" git clone --depth 1 --quiet "$REPO_URL" "$tmp_dir"

    section "Installing Application Files"
    mkdir -p "$INSTALL_DIR"/{daemon,api,rules,etc,bin}
    mkdir -p "$INSTALL_DIR"/lib/{watcher,scanner,bruteforce,waf,proactive,cleanup,threat_intel,webshield,ufw,crowdsec}
    cp "$tmp_dir"/daemon/*.py "$INSTALL_DIR/daemon/"
    cp "$tmp_dir"/lib/*.py "$INSTALL_DIR/lib/"
    cp "$tmp_dir"/lib/watcher/*.py "$INSTALL_DIR/lib/watcher/"
    cp "$tmp_dir"/lib/scanner/*.py "$INSTALL_DIR/lib/scanner/"
    cp "$tmp_dir"/lib/bruteforce/*.py "$INSTALL_DIR/lib/bruteforce/"
    cp "$tmp_dir"/lib/waf/*.py "$INSTALL_DIR/lib/waf/"
    cp "$tmp_dir"/lib/proactive/*.py "$INSTALL_DIR/lib/proactive/"
    cp "$tmp_dir"/lib/cleanup/*.py "$INSTALL_DIR/lib/cleanup/"
    cp "$tmp_dir"/lib/threat_intel/*.py "$INSTALL_DIR/lib/threat_intel/"
    cp "$tmp_dir"/lib/webshield/*.py "$INSTALL_DIR/lib/webshield/"
    cp "$tmp_dir"/lib/ufw/*.py "$INSTALL_DIR/lib/ufw/"
    cp "$tmp_dir"/lib/crowdsec/*.py "$INSTALL_DIR/lib/crowdsec/"
    cp "$tmp_dir"/api/*.py "$INSTALL_DIR/api/"
    mkdir -p "$INSTALL_DIR/api/routes"
    cp "$tmp_dir"/api/routes/*.py "$INSTALL_DIR/api/routes/"
    cp "$tmp_dir"/rules/*.yar "$INSTALL_DIR/rules/"
    cp "$tmp_dir"/etc/jabali-security.conf.example "$INSTALL_DIR/etc/"
    cp "$tmp_dir"/etc/jabali-security.service "$INSTALL_DIR/etc/"
    cp -r "$tmp_dir"/etc/webshield "$INSTALL_DIR/etc/" 2>/dev/null || true
    mkdir -p "$INSTALL_DIR/etc/sudoers.d"
    cp "$tmp_dir"/etc/sudoers.d/jabali-security "$INSTALL_DIR/etc/sudoers.d/" 2>/dev/null || true
    cp "$tmp_dir"/bin/jabali-security "$INSTALL_DIR/bin/"
    chmod +x "$INSTALL_DIR/bin/jabali-security"

    # Deploy shared challenge page and njs validator
    mkdir -p /etc/nginx/jabali/challenge /etc/nginx/jabali-security
    cp "$INSTALL_DIR/etc/webshield/challenge.html" /etc/nginx/jabali/challenge/jabali-challenge.html 2>/dev/null || true
    cp "$INSTALL_DIR/etc/webshield/jabali_challenge.js" /etc/nginx/jabali-security/jabali_challenge.js 2>/dev/null || true

    done_ok "Application files installed"

    # -- Jabali Panel integration (Filament plugin) --
    JABALI_PANEL_DIR="/var/www/jabali"
    if [ -d "$JABALI_PANEL_DIR/app/Filament" ]; then
        section "Installing Jabali Panel Plugin"
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/Pages"
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/Widgets"
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/views"
        cp "$tmp_dir"/panel/JabaliSecurityPlugin.php "$JABALI_PANEL_DIR/app/JabaliSecurity/"
        cp "$tmp_dir"/panel/JabaliSecurityClient.php "$JABALI_PANEL_DIR/app/JabaliSecurity/"
        cp "$tmp_dir"/panel/Pages/*.php "$JABALI_PANEL_DIR/app/JabaliSecurity/Pages/"
        cp "$tmp_dir"/panel/Widgets/*.php "$JABALI_PANEL_DIR/app/JabaliSecurity/Widgets/"
        cp "$tmp_dir"/panel/views/*.blade.php "$JABALI_PANEL_DIR/app/JabaliSecurity/views/"

        # AdminPanelProvider uses class_exists() guard — no injection needed

        done_ok "Jabali Panel plugin installed"
    fi

    # Clean up temp clone
    rm -rf "$tmp_dir"

    # -- CLI symlink --
    ln -sf "$INSTALL_DIR/bin/jabali-security" /usr/local/bin/jabali-security

    # -- Create dedicated service user (privilege separation) --
    section "Creating Service User"
    if ! id jabali-security &>/dev/null; then
        useradd --system --no-create-home --shell /usr/sbin/nologin \
            --home-dir "$INSTALL_DIR" jabali-security
        echo "  Created system user: jabali-security"
    else
        echo "  User jabali-security already exists."
    fi
    # Add to www-data group for socket access
    if id www-data &>/dev/null; then
        usermod -aG www-data jabali-security 2>/dev/null || true
    fi
    done_ok "Service user configured"

    # -- Install sudoers rules --
    section "Installing Sudoers Rules"
    if [ -d /etc/sudoers.d ]; then
        cp "$INSTALL_DIR/etc/sudoers.d/jabali-security" /etc/sudoers.d/jabali-security
        chmod 440 /etc/sudoers.d/jabali-security
        chown root:root /etc/sudoers.d/jabali-security
        if visudo -cf /etc/sudoers.d/jabali-security >/dev/null 2>&1; then
            echo "  Sudoers rules installed and validated."
        else
            rm -f /etc/sudoers.d/jabali-security
            yellow "  WARNING: sudoers file failed validation, removed. Daemon will need root."
        fi
    fi
    done_ok "Sudoers configured"

    section "Configuring Directories & Permissions"
    mkdir -p "$CONFIG_DIR"
    chown jabali-security:www-data "$CONFIG_DIR" 2>/dev/null || chown jabali-security: "$CONFIG_DIR"
    chmod 750 "$CONFIG_DIR"
    mkdir -p "$LOG_DIR"
    chown jabali-security: "$LOG_DIR"
    chmod 750 "$LOG_DIR"
    mkdir -p "$DATA_DIR"
    chown jabali-security: "$DATA_DIR"
    chmod 700 "$DATA_DIR"
    mkdir -p "$QUARANTINE_DIR"
    chown jabali-security: "$QUARANTINE_DIR"
    chmod 700 "$QUARANTINE_DIR"
    # Nginx config dirs (daemon writes GeoIP/WebShield configs here)
    mkdir -p /etc/nginx/jabali/cache-zones /etc/nginx/jabali/includes
    chown -R jabali-security: /etc/nginx/jabali/ 2>/dev/null || true
    mkdir -p /etc/nginx/jabali-security
    chown jabali-security: /etc/nginx/jabali-security 2>/dev/null || true

    done_ok "Directories created"

    section "Configuring Security Daemon"
    # Always ensure correct permissions (even on reinstall)
    chown jabali-security:www-data "$CONFIG_DIR" 2>/dev/null || chown jabali-security: "$CONFIG_DIR"
    chmod 750 "$CONFIG_DIR" 2>/dev/null

    if [ ! -f "$CONFIG_DIR/jabali-security.conf" ]; then
        cp "$INSTALL_DIR/etc/jabali-security.conf.example" "$CONFIG_DIR/jabali-security.conf"
        chown jabali-security:www-data "$CONFIG_DIR/jabali-security.conf" 2>/dev/null || chown jabali-security: "$CONFIG_DIR/jabali-security.conf"
        chmod 640 "$CONFIG_DIR/jabali-security.conf"
        echo "  Config: $CONFIG_DIR/jabali-security.conf"
    else
        echo "  Config already exists, keeping current."
        # Fix permissions on existing config (upgrade from root ownership)
        chown jabali-security:www-data "$CONFIG_DIR/jabali-security.conf" 2>/dev/null || chown jabali-security: "$CONFIG_DIR/jabali-security.conf"
        chmod 640 "$CONFIG_DIR/jabali-security.conf" 2>/dev/null
    fi

    # -- Generate API_KEY if not set --
    if ! grep -q "^API_KEY=" "$CONFIG_DIR/jabali-security.conf" 2>/dev/null || \
       grep -q '^API_KEY=""' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null; then
        # Generate key and write directly to config (avoid exposing in /proc/*/cmdline)
        python3 -c "
import secrets, re, sys
key = secrets.token_urlsafe(32)
path = sys.argv[1]
with open(path) as f: content = f.read()
if 'API_KEY=' in content:
    content = re.sub(r'^API_KEY=.*', 'API_KEY=\"' + key + '\"', content, flags=re.MULTILINE)
else:
    content += '\nAPI_KEY=\"' + key + '\"\n'
with open(path, 'w') as f: f.write(content)
" "$CONFIG_DIR/jabali-security.conf"
        chown jabali-security:www-data "$CONFIG_DIR/jabali-security.conf" 2>/dev/null || chown jabali-security: "$CONFIG_DIR/jabali-security.conf"
        chmod 640 "$CONFIG_DIR/jabali-security.conf"
        echo "  API key generated."
    fi

    # Generate CrowdSec bouncer API key and set LAPI URL
    if command -v cscli &>/dev/null; then
        if ! grep -q '^CROWDSEC_BOUNCER_KEY="..*"' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null; then
            # Delete existing bouncer first (reinstall leaves stale entry, add fails silently)
            cscli bouncers delete jabali-security 2>/dev/null || true
            bouncer_key=$(cscli bouncers add jabali-security -o raw 2>/dev/null || echo "")
            if [ -n "$bouncer_key" ]; then
                if grep -q "^CROWDSEC_BOUNCER_KEY=" "$CONFIG_DIR/jabali-security.conf"; then
                    sed -i "s|^CROWDSEC_BOUNCER_KEY=.*|CROWDSEC_BOUNCER_KEY=\"${bouncer_key}\"|" "$CONFIG_DIR/jabali-security.conf"
                else
                    echo "CROWDSEC_BOUNCER_KEY=\"${bouncer_key}\"" >> "$CONFIG_DIR/jabali-security.conf"
                fi
                echo "  CrowdSec bouncer key generated."
            fi
        fi
    fi

    # -- Set Unix socket path if not already present --
    if ! grep -q "^API_SOCKET=" "$CONFIG_DIR/jabali-security.conf" 2>/dev/null; then
        echo 'API_SOCKET="/run/jabali-security/jabali-security.sock"' >> "$CONFIG_DIR/jabali-security.conf"
    fi
    # Disable TCP by default (Unix socket is primary)
    sed -i 's|^API_BIND="127.0.0.1"|API_BIND=""|' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null

    done_ok "Daemon configured"

    section "Configuring WAF (ModSecurity)"
    CRS_DIR=""
    for d in /usr/local/share/owasp-crs/rules /usr/share/modsecurity-crs/rules /etc/modsecurity/crs /usr/share/modsecurity-crs; do
        if [ -d "$d" ] && ls "$d"/*.conf &>/dev/null; then
            CRS_DIR="$d"
            break
        fi
    done
    if [ -n "$CRS_DIR" ]; then
        echo "  OWASP CRS found at $CRS_DIR"
        if ! grep -q "^WAF_RULES_DIR=" "$CONFIG_DIR/jabali-security.conf" 2>/dev/null; then
            echo "WAF_RULES_DIR=\"$CRS_DIR\"" >> "$CONFIG_DIR/jabali-security.conf"
        else
            sed -i "s|^WAF_RULES_DIR=.*|WAF_RULES_DIR=\"$CRS_DIR\"|" "$CONFIG_DIR/jabali-security.conf"
        fi
    fi

    if [ -f /etc/nginx/modsecurity.conf ]; then
        sed -i 's/SecRuleEngine DetectionOnly/SecRuleEngine On/' /etc/nginx/modsecurity.conf
        echo "  ModSecurity set to blocking mode."

        sed -i 's|^WAF_AUDIT_LOG=.*|WAF_AUDIT_LOG="/var/log/nginx/modsec_audit.log"|' "$CONFIG_DIR/jabali-security.conf"

        if [ -n "$CRS_DIR" ] && [ -f /etc/nginx/modsecurity_includes.conf ]; then
            CRS_SETUP=""
            for f in /usr/local/share/owasp-crs/crs-setup.conf /etc/modsecurity/crs/crs-setup.conf /usr/share/modsecurity-crs/crs-setup.conf; do
                if [ -f "$f" ]; then CRS_SETUP="$f"; break; fi
            done
            if [ -n "$CRS_SETUP" ]; then
                cat > /etc/nginx/modsecurity_includes.conf << MODSECEOF
include modsecurity.conf
include $CRS_SETUP
include $CRS_DIR/*.conf
MODSECEOF
                echo "  CRS rules configured for nginx."
            fi
        fi

        if ! grep -q "modsecurity on" /etc/nginx/nginx.conf 2>/dev/null; then
            sed -i '/http {/a\\tmodsecurity on;\n\tmodsecurity_rules_file /etc/nginx/modsecurity_includes.conf;' /etc/nginx/nginx.conf
            echo "  ModSecurity enabled in nginx."
        fi

        # Enable WAF per-site in the nginx include
        WAF_INCLUDE_DIR="/etc/nginx/jabali/includes"
        mkdir -p "$WAF_INCLUDE_DIR"
        cat > "$WAF_INCLUDE_DIR/waf.conf" << 'WAFEOF'
# Managed by Jabali Security
modsecurity on;

# Security headers
add_header X-Frame-Options "SAMEORIGIN" always;
add_header X-Content-Type-Options "nosniff" always;
add_header X-XSS-Protection "1; mode=block" always;
add_header Referrer-Policy "strict-origin-when-cross-origin" always;
add_header Content-Security-Policy "default-src 'self' https: data: 'unsafe-inline' 'unsafe-eval'" always;
WAFEOF
        echo "  WAF per-site include ...... enabled"

        if nginx -t 2>/dev/null; then
            systemctl reload nginx 2>/dev/null || true
            done_ok "WAF configured (ModSecurity + OWASP CRS)"
        else
            red "  WARNING: nginx config test failed after ModSecurity setup."
        fi
    else
        echo "  ModSecurity not found, skipping WAF setup."
    fi

    section "Enabling Protection Modules"
    if command -v nft &>/dev/null || command -v iptables &>/dev/null; then
        sed -i 's|^BRUTEFORCE_ENABLED="no"|BRUTEFORCE_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf"
        echo "  Brute-force protection .... enabled"
    fi
    if [ -n "$CRS_DIR" ]; then
        sed -i 's|^WAF_ENABLED="no"|WAF_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf"
        echo "  WAF (ModSecurity) ........ enabled"
    fi
    sed -i 's|^PROCESS_KILL_ENABLED="no"|PROCESS_KILL_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null
    echo "  Process Killer ........... enabled"
    sed -i 's|^THREAT_INTEL_ENABLED="no"|THREAT_INTEL_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null
    echo "  Threat Intelligence ...... enabled"
    # WebShield: auto-enable on Jabali panel servers (known nginx structure)
    if [ -d "/var/www/jabali" ] && [ -d "/etc/nginx/jabali/includes" ]; then
        # Generate WebShield configs via the daemon API (must be running)
        _ws_ok=false
        _api_key=$(grep -oP '^API_KEY="\K[^"]+' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null || echo "")
        _sock="/run/jabali-security/jabali-security.sock"
        if [ -S "$_sock" ] && [ -n "$_api_key" ]; then
            _ws_result=$(curl -sf --unix-socket "$_sock" -X POST -H "X-API-Key: $_api_key" \
                "http://localhost/api/v1/webshield/install" 2>/dev/null || echo "")
            if echo "$_ws_result" | grep -q '"success": true'; then
                _ws_ok=true
            fi
        fi

        if [ "$_ws_ok" = true ]; then
            # Bridge: per-site include points to the webshield server config
            cat > /etc/nginx/jabali/includes/webshield.conf << 'WSEOF'
# Managed by Jabali Security — WebShield per-site config
include /etc/nginx/jabali-security/jabali-webshield-server.conf;
WSEOF
            # Validate before enabling
            if nginx -t 2>/dev/null; then
                sed -i 's|^WEBSHIELD_ENABLED="no"|WEBSHIELD_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf"
                systemctl reload nginx 2>/dev/null || true
                echo "  WebShield ................ enabled"
            else
                # Rollback
                rm -f /etc/nginx/jabali/includes/webshield.conf
                echo "  WebShield ................ disabled (nginx config test failed)"
            fi
        else
            echo "  WebShield ................ disabled (daemon not ready, enable via panel)"
        fi
    else
        echo "  WebShield ................ disabled (enable via panel)"
    fi
    sed -i 's|^CLEANUP_ENABLED="no"|CLEANUP_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null
    echo "  Auto Cleanup ............. enabled"
    done_ok "Protection modules configured"

    section "Configuring Firewall (UFW)"
    if command -v ufw &>/dev/null; then
        sed -i 's|^UFW_ENABLED="no"|UFW_ENABLED="yes"|' "$CONFIG_DIR/jabali-security.conf" 2>/dev/null
        ufw default deny incoming >/dev/null 2>&1 || true
        ufw default allow outgoing >/dev/null 2>&1 || true

        # Open standard hosting ports
        _open_port() { ufw allow "$1" comment "$2" >/dev/null 2>&1 || true; printf "  %-8s %-18s %s\n" "$1" "$2" "$3"; }

        echo ""
        printf "  %-8s %-18s %s\n" "PORT" "SERVICE" "NOTES"
        printf "  %-8s %-18s %s\n" "────" "──────────────────" "────────────────────────────"
        _open_port "22/tcp"   "SSH"                  ""
        _open_port "80/tcp"   "HTTP"                 "ACME, autoconfig, redirects"
        _open_port "443/tcp"  "HTTPS"                "Sites, webmail, phpMyAdmin"
        _open_port "25/tcp"   "SMTP"                 "Other mail servers connect here"
        _open_port "465/tcp"  "SMTPS"                "Mail clients"
        _open_port "587/tcp"  "Submission"            "Mail clients"
        _open_port "110/tcp"  "POP3"                 "Mail clients"
        _open_port "143/tcp"  "IMAP"                 "Mail clients"
        _open_port "993/tcp"  "IMAPS"                "Mail clients"
        _open_port "995/tcp"  "POP3S"                "Mail clients"
        if command -v pdns_server &>/dev/null || [ -d "/etc/powerdns" ]; then
            _open_port "53/tcp" "DNS"                "PowerDNS detected"
            _open_port "53/udp" "DNS"                "PowerDNS detected"
        fi
        if [ -d "/var/www/jabali" ]; then
            _panel_port=$(grep -oP '^PANEL_PORT=\K[0-9]+' /var/www/jabali/.env 2>/dev/null || echo "8443")
            _open_port "${_panel_port}/tcp" "Jabali Panel" "Admin + user panel"
        fi
        echo ""

        ufw --force enable 2>&1 | sed 's/^/  /'
        done_ok "Firewall configured"
    else
        echo "  UFW not available, skipping firewall setup."
    fi

    section "Hardening SSH (Jail Environment)"
    (
    # Run jail setup in subshell so failures don't kill the installer
    set +e

    # Create groups for SFTP and shell users
    groupadd sftpusers 2>/dev/null || true
    groupadd shellusers 2>/dev/null || true

    # Backup and configure sshd_config
    if [ -f /etc/ssh/sshd_config ]; then
        cp /etc/ssh/sshd_config "/etc/ssh/sshd_config.backup.$(date +%Y%m%d%H%M%S)"
        echo "  sshd_config backed up"

        # Disable password authentication (key-based auth only)
        if grep -q "^PasswordAuthentication yes" /etc/ssh/sshd_config; then
            sed -i 's/^PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
            echo "  Password authentication disabled (key-based only)"
        elif ! grep -q "^PasswordAuthentication" /etc/ssh/sshd_config; then
            echo "PasswordAuthentication no" >> /etc/ssh/sshd_config
            echo "  Password authentication disabled (key-based only)"
        fi

        # Remove PasswordAuthentication overrides from sshd_config.d/ drop-ins
        # (e.g. cloud-init sets PasswordAuthentication yes which takes precedence)
        for dropfile in /etc/ssh/sshd_config.d/*.conf; do
            [ -f "$dropfile" ] || continue
            if grep -q "^PasswordAuthentication" "$dropfile"; then
                sed -i '/^PasswordAuthentication/d' "$dropfile"
                echo "  Removed PasswordAuthentication override from $(basename "$dropfile")"
            fi
        done

        # Remove any existing Jabali SSH config block
        sed -i '/# Jabali SSH Jail Configuration/,/# End Jabali SSH Jail/d' /etc/ssh/sshd_config

        # Use internal-sftp (works with ChrootDirectory, no external binary needed)
        sed -i 's|Subsystem\tsftp\t/usr/lib/openssh/sftp-server|Subsystem\tsftp\tinternal-sftp|' /etc/ssh/sshd_config

        # Append SSH jail configuration
        cat >> /etc/ssh/sshd_config << 'SSHJAIL'

# Jabali SSH Jail Configuration
LoginGraceTime 60
MaxStartups 10:30:60

# SFTP-only users (default for all panel users)
Match Group sftpusers
    ChrootDirectory /home/%u
    ForceCommand internal-sftp
    PasswordAuthentication no
    AllowTcpForwarding no
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    PermitTTY no
    PermitTunnel no
    PermitOpen none
    PermitListen none
    PermitUserRC no
    X11Forwarding no
    MaxSessions 2
    ClientAliveInterval 300
    ClientAliveCountMax 2

# Shell users (isolated via jabali-isolator nspawn containers)
Match Group shellusers
    ForceCommand /usr/local/bin/jabali-shell
    PasswordAuthentication no
    AllowTcpForwarding no
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    PermitTunnel no
    PermitOpen none
    PermitListen none
    PermitUserRC no
    X11Forwarding no
    MaxSessions 5
    ClientAliveInterval 300
    ClientAliveCountMax 3
# End Jabali SSH Jail
SSHJAIL
        echo "  SSH rules added (sftpusers + shellusers via nspawn)"
    fi

    # Validate config before restarting SSH
    if sshd -t 2>/dev/null; then
        systemctl restart sshd 2>/dev/null || systemctl restart ssh 2>/dev/null || true
    else
        red "WARNING: sshd config test failed! SSH not restarted. Fix /etc/ssh/sshd_config manually."
    fi
    ) || true
    done_ok "SSH hardened (SFTP chroot + shell via nspawn)"

    section "System Tuning"
    current_watches=$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo 0)
    if [ "$current_watches" -lt 524288 ]; then
        echo "fs.inotify.max_user_watches=524288" > "$SYSCTL_CONF"
        sysctl -p "$SYSCTL_CONF" 2>/dev/null || true
        echo "  inotify watch limit raised to 524288"
    else
        echo "  inotify watch limit OK ($current_watches)"
    fi
    done_ok "System tuning applied"

    section "Installing Python Dependencies"
    _venv_dir="$INSTALL_DIR/venv"
    if [ ! -d "$_venv_dir" ] || [ ! -f "$_venv_dir/bin/python" ]; then
        if ! python3 -m venv "$_venv_dir" 2>/dev/null; then
            run_with_spinner "Installing python3-venv" pkg_install python3-venv
            python3 -m venv "$_venv_dir"
        fi
    fi

    if [ -f "$_venv_dir/bin/pip" ]; then
        local pip_pkgs="pydantic>=2.0 yara-x>=0.11 click>=8.0 aiohttp>=3.9 pyyaml>=6.0 aiosqlite>=0.20 maxminddb>=2.0"
        if command -v uv &>/dev/null; then
            # shellcheck disable=SC2086
            run_with_spinner "Installing Python packages (uv)" \
                uv pip install --python "$_venv_dir/bin/python" $pip_pkgs
        else
            # shellcheck disable=SC2086
            run_with_spinner "Installing Python packages (pip)" \
                "$_venv_dir/bin/pip" install --quiet --no-cache-dir $pip_pkgs
        fi
    fi

    section "Starting Services"
    cp "$INSTALL_DIR/etc/jabali-security.service" /etc/systemd/system/
    systemctl daemon-reload 2>/dev/null || true
    systemctl enable "$SERVICE_NAME" 2>/dev/null || true
    systemctl restart "$SERVICE_NAME" 2>/dev/null || true
    echo "  jabali-security daemon .... started"

    done_ok "Services started"

    # ── Summary ───────────────────────────────────────────────────────
    echo ""
    echo ""
    yellow "  Installation Complete"
    yellow "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo ""
    green "  Jabali Security installed successfully!"
    echo ""
    echo "  Daemon:    systemctl status $SERVICE_NAME"
    echo "  Config:    $CONFIG_DIR/jabali-security.conf"
    echo "  CLI:       jabali-security --help"
    echo "  Logs:      journalctl -u $SERVICE_NAME -f"
    echo ""
}

# ── Update ────────────────────────────────────────────────────────────────

do_update() {
    require_root
    yellow "Updating Jabali Security..."

    if [ ! -d "$INSTALL_DIR" ]; then
        red "Error: Jabali Security is not installed at $INSTALL_DIR"
        red "Run the installer without --update to install first."
        exit 1
    fi

    # Clone latest code
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    echo "Pulling latest code..."
    git clone --depth 1 --quiet "$REPO_URL" "$tmp_dir"

    # Stop services before updating files
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    # -- Update application files (preserve config/data/venv) --
    cp "$tmp_dir"/daemon/*.py "$INSTALL_DIR/daemon/"
    cp "$tmp_dir"/lib/*.py "$INSTALL_DIR/lib/"
    for subdir in watcher scanner bruteforce waf proactive cleanup threat_intel webshield ufw crowdsec; do
        mkdir -p "$INSTALL_DIR/lib/$subdir"
        cp "$tmp_dir"/lib/$subdir/*.py "$INSTALL_DIR/lib/$subdir/" 2>/dev/null || true
    done
    mkdir -p "$INSTALL_DIR/api/routes"
    cp "$tmp_dir"/api/*.py "$INSTALL_DIR/api/"
    cp "$tmp_dir"/api/routes/*.py "$INSTALL_DIR/api/routes/"
    cp "$tmp_dir"/rules/*.yar "$INSTALL_DIR/rules/" 2>/dev/null || true
    cp "$tmp_dir"/etc/jabali-security.service "$INSTALL_DIR/etc/"
    cp -r "$tmp_dir"/etc/webshield "$INSTALL_DIR/etc/" 2>/dev/null || true
    cp "$tmp_dir"/bin/jabali-security "$INSTALL_DIR/bin/"
    chmod +x "$INSTALL_DIR/bin/jabali-security"

    # Deploy shared challenge page and njs validator
    mkdir -p /etc/nginx/jabali/challenge /etc/nginx/jabali-security
    cp "$INSTALL_DIR/etc/webshield/challenge.html" /etc/nginx/jabali/challenge/jabali-challenge.html 2>/dev/null || true
    cp "$INSTALL_DIR/etc/webshield/jabali_challenge.js" /etc/nginx/jabali-security/jabali_challenge.js 2>/dev/null || true

    # -- Ensure Python dependencies are up to date --
    local _venv_dir="$INSTALL_DIR/venv"
    if [ -f "$_venv_dir/bin/pip" ]; then
        local pip_pkgs="pydantic>=2.0 yara-x>=0.11 click>=8.0 aiohttp>=3.9 pyyaml>=6.0 aiosqlite>=0.20 maxminddb>=2.0"
        if command -v uv >/dev/null 2>&1; then
            uv pip install --python "$_venv_dir/bin/python" $pip_pkgs 2>/dev/null || true
        else
            "$_venv_dir/bin/pip" install --quiet $pip_pkgs 2>/dev/null || true
        fi
    fi

    # -- Update Jabali Panel plugin if panel exists --
    JABALI_PANEL_DIR="/var/www/jabali"
    if [ -d "$JABALI_PANEL_DIR/app/Filament" ] && [ -d "$tmp_dir/panel" ]; then
        echo "Updating Jabali Panel security plugin..."
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/Pages"
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/Widgets"
        mkdir -p "$JABALI_PANEL_DIR/app/JabaliSecurity/views"
        cp "$tmp_dir"/panel/JabaliSecurityPlugin.php "$JABALI_PANEL_DIR/app/JabaliSecurity/"
        cp "$tmp_dir"/panel/JabaliSecurityClient.php "$JABALI_PANEL_DIR/app/JabaliSecurity/"
        cp "$tmp_dir"/panel/Pages/*.php "$JABALI_PANEL_DIR/app/JabaliSecurity/Pages/"
        cp "$tmp_dir"/panel/Widgets/*.php "$JABALI_PANEL_DIR/app/JabaliSecurity/Widgets/"
        cp "$tmp_dir"/panel/views/*.blade.php "$JABALI_PANEL_DIR/app/JabaliSecurity/views/"
        # Clear Laravel caches so Filament discovers updated plugin classes
        composer dump-autoload -q -d "$JABALI_PANEL_DIR" 2>/dev/null || true
        php "$JABALI_PANEL_DIR/artisan" filament:cache-components 2>/dev/null || true
        php "$JABALI_PANEL_DIR/artisan" view:clear 2>/dev/null || true
        echo "Panel plugin updated."
    fi

    # Clean up
    rm -rf "$tmp_dir"

    # -- Remove PasswordAuthentication overrides from sshd_config.d/ drop-ins --
    for dropfile in /etc/ssh/sshd_config.d/*.conf; do
        [ -f "$dropfile" ] || continue
        if grep -q "^PasswordAuthentication" "$dropfile"; then
            sed -i '/^PasswordAuthentication/d' "$dropfile"
            echo "  Removed PasswordAuthentication override from $(basename "$dropfile")"
            systemctl reload sshd 2>/dev/null || systemctl reload ssh 2>/dev/null || true
        fi
    done

    # -- Patch sshd_config: migrate chroot → nspawn + ensure PasswordAuthentication --
    if [ -f /etc/ssh/sshd_config ] && grep -q "Jabali SSH Jail" /etc/ssh/sshd_config; then
        local needs_patch=false
        # Migrate old ChrootDirectory /var/jail to ForceCommand /usr/local/bin/jabali-shell
        if grep -q "ChrootDirectory /var/jail" /etc/ssh/sshd_config; then
            needs_patch=true
        fi
        # Also patch if PasswordAuthentication is missing from Match blocks
        if ! sed -n '/Match Group sftpusers/,/Match Group\|# End/p' /etc/ssh/sshd_config | grep -q "PasswordAuthentication"; then
            needs_patch=true
        fi
        if [ "$needs_patch" = true ] && command -v jabali-shell >/dev/null 2>&1; then
            echo "Patching sshd_config: migrating to nspawn isolation..."
            sed -i '/# Jabali SSH Jail Configuration/,/# End Jabali SSH Jail/d' /etc/ssh/sshd_config
            cat >> /etc/ssh/sshd_config << 'SSHJAIL'

# Jabali SSH Jail Configuration
LoginGraceTime 60
MaxStartups 10:30:60

# SFTP-only users (default for all panel users)
Match Group sftpusers
    ChrootDirectory /home/%u
    ForceCommand internal-sftp
    PasswordAuthentication no
    AllowTcpForwarding no
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    PermitTTY no
    PermitTunnel no
    PermitOpen none
    PermitListen none
    PermitUserRC no
    X11Forwarding no
    MaxSessions 2
    ClientAliveInterval 300
    ClientAliveCountMax 2

# Shell users (isolated via jabali-isolator nspawn containers)
Match Group shellusers
    ForceCommand /usr/local/bin/jabali-shell
    PasswordAuthentication no
    AllowTcpForwarding no
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    PermitTunnel no
    PermitOpen none
    PermitListen none
    PermitUserRC no
    X11Forwarding no
    MaxSessions 5
    ClientAliveInterval 300
    ClientAliveCountMax 3
# End Jabali SSH Jail
SSHJAIL
            if sshd -t 2>/dev/null; then
                systemctl reload sshd 2>/dev/null || systemctl reload ssh 2>/dev/null || true
                echo "  sshd_config patched and reloaded."
            else
                echo "  WARNING: sshd config test failed after patching!"
            fi
        fi
    fi

    # -- Clean up old /var/jail chroot (replaced by nspawn) --
    if [ -d /var/jail ] && command -v jabali-shell >/dev/null 2>&1; then
        echo "Cleaning up old /var/jail chroot (replaced by nspawn)..."
        # Unmount jail filesystems
        umount /var/jail/tmp 2>/dev/null || true
        umount /var/jail/proc 2>/dev/null || true
        # Unmount any user bind mounts
        for mnt in /var/jail/home/*/; do
            [ -d "$mnt" ] && umount "$mnt" 2>/dev/null || true
        done
        # Remove fstab entries
        if [ -f /etc/fstab ]; then
            sed -i '/# jabali-jail-proc/d' /etc/fstab
            sed -i '/# jabali-jail-tmp/d' /etc/fstab
            sed -i '/# jabali-ssh:/d' /etc/fstab
        fi
        # Remove the directory
        rm -rf /var/jail 2>/dev/null || true
        echo "  Old /var/jail removed."
    fi

    # -- Update systemd service files --
    cp "$INSTALL_DIR/etc/jabali-security.service" /etc/systemd/system/
    systemctl daemon-reload 2>/dev/null || true

    # -- Restart services --
    systemctl start "$SERVICE_NAME" 2>/dev/null || true

    echo ""
    green "Jabali Security updated successfully!"
    echo "  Version: $(grep 'VERSION = ' "$INSTALL_DIR/lib/constants.py" 2>/dev/null | cut -d'"' -f2 || echo '?')"
}

# ── Main ───────────────────────────────────────────────────────────────────

case "${1:-}" in
    --uninstall)
        do_uninstall
        ;;
    --update)
        do_update
        ;;
    --help|-h)
        echo "Usage: $0 [--uninstall|--update]"
        echo ""
        echo "  (no args)     Install Jabali Security"
        echo "  --update      Update to latest version (preserves config/data)"
        echo "  --uninstall   Completely remove Jabali Security (config, data, logs)"
        ;;
    "")
        do_install
        ;;
    *)
        red "Unknown option: $1"
        echo "Usage: $0 [--uninstall|--update]"
        exit 1
        ;;
esac
