#!/bin/bash
#
# Jabali Panel Installer
#
# Quick install (if curl/sudo are available):
#   curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
#
# Minimal installation (if curl/sudo not installed):
#   apt-get update && apt-get install -y curl sudo
#   curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
#
set -e

# Ensure sbin directories are in PATH (some minimal images or piped installs miss them)
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"

# Version - prefer local VERSION file if present, fallback for curl installs
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "$SCRIPT_DIR/VERSION" ]]; then
    JABALI_VERSION="$(sed -n 's/^VERSION=//p' "$SCRIPT_DIR/VERSION")"
fi
JABALI_VERSION="${JABALI_VERSION:-0.9-rc122}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Verbosity (set via --debug flag)
DEBUG=${DEBUG:-false}

# Configuration
JABALI_DIR="/var/www/jabali"
JABALI_USER="www-data"
JABALI_REPO="https://github.com/shukiv/jabali-panel.git"
JABALI_BRANCH="main"
PANEL_PORT="${PANEL_PORT:-8443}"
NODE_VERSION="20"

# PHP version will be detected after installation
PHP_VERSION=""

# Feature flags (default: all enabled)
INSTALL_MAIL=true
INSTALL_DNS=true

# Mail backend: Stalwart is the only supported backend
MAIL_BACKEND="stalwart"

# Feature selection menu
select_features() {
    echo ""
    echo -e "${BOLD}Installation Options${NC}"
    echo ""
    echo "Jabali Panel includes optional components that can be installed based on your needs."
    echo "Each component uses additional server resources."
    echo ""

    # Check if non-interactive mode
    if [[ -n "$JABALI_MINIMAL" ]]; then
        info "Minimal installation mode: Core components + DNS will be installed"
        INSTALL_MAIL=false
        return
    fi

    if [[ -n "$JABALI_FULL" ]]; then
        info "Full installation mode: All components will be installed"
        return
    fi

    echo "Select installation type:"
    echo ""
    echo "  1) Full Installation (Recommended)"
    echo "     - Web Server (Nginx, PHP, MariaDB, Redis)"
    echo "     - Mail Server"
    echo "     - DNS Server (PowerDNS)"
    echo ""
    echo "  2) Minimal Installation"
    echo "     - Web Server only (Nginx, PHP, MariaDB, Redis)"
    echo "     - DNS Server (PowerDNS)"
    echo ""
    echo "  3) Custom Installation"
    echo "     - Choose individual components"
    echo ""

    local choice
    read -p "Enter choice [1-3]: " choice < /dev/tty

    case $choice in
        1)
            info "Full installation selected"
            ;;
        2)
            info "Minimal installation selected"
            INSTALL_MAIL=false
            ;;
        3)
            echo ""
            echo -e "${BOLD}Custom Installation${NC}"
            echo ""

            # Mail Server
            read -p "Install Mail Server? [Y/n]: " mail_choice < /dev/tty
            if [[ "$mail_choice" =~ ^[Nn]$ ]]; then
                INSTALL_MAIL=false
            fi

            # DNS Server
            read -p "Install DNS Server (PowerDNS)? [Y/n]: " dns_choice < /dev/tty
            if [[ "$dns_choice" =~ ^[Nn]$ ]]; then
                INSTALL_DNS=false
            fi

            echo ""
            info "Custom installation configured"
            ;;
        *)
            info "Defaulting to full installation"
            ;;
    esac

    echo ""
    echo -e "${BOLD}Components to install:${NC}"
    echo -e "  - Web Server: ${GREEN}Yes${NC}"
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        echo -e "  - Mail Server: ${GREEN}Yes${NC} (Stalwart)"
    else
        echo -e "  - Mail Server: ${YELLOW}No${NC}"
    fi
    [[ "$INSTALL_DNS" == "true" ]] && echo -e "  - DNS Server: ${GREEN}Yes${NC}" || echo -e "  - DNS Server: ${YELLOW}No${NC}"
    echo ""
}

# Detect installed PHP version
detect_php_version() {
    # Try to find the highest installed PHP version
    if command -v php &> /dev/null; then
        PHP_VERSION=$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;' 2>/dev/null)
    fi

    # Fallback: check for PHP-FPM sockets
    if [[ -z "$PHP_VERSION" ]]; then
        for ver in 8.5 8.4 8.3 8.2 8.1 8.0; do
            if [[ -f "/etc/php/${ver}/fpm/php.ini" ]]; then
                PHP_VERSION="$ver"
                break
            fi
        done
    fi

    if [[ -z "$PHP_VERSION" ]]; then
        error "Could not detect PHP version. Please ensure PHP is installed."
    fi

    info "Detected PHP version: $PHP_VERSION"
}

# Logging
log() {
    echo -e "${GREEN}[✓]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[!]${NC} $1"
}

error() {
    echo -e "${RED}[✗]${NC} $1"
    exit 1
}

info() {
    echo -e "${CYAN}[i]${NC} $1"
}

header() {
    echo ""
    echo -e "${BOLD}${BLUE}=== $1 ===${NC}"
    echo ""
}

# Start a spinner on the current line. Call stop_spinner to finish.
# The spinner runs as a separate bash process (not a subshell) so it can
# be reliably killed. Output goes to stderr to avoid interfering with pipes.
_spinner_pid=""

_spinner_running=""

start_spinner() {
    local label="$1"
    _spinner_running=$(mktemp /tmp/.jabali-spinner-XXXXXX)
    (
        local frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)
        local n=${#frames[@]} i=0
        tput civis 2>/dev/null || true
        while [[ -f "$_spinner_running" ]]; do
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
    # Signal spinner to stop by removing the flag file
    rm -f "$_spinner_running" 2>/dev/null
    if [[ -n "$_spinner_pid" ]]; then
        # Wait for spinner loop to notice the file is gone
        wait "$_spinner_pid" 2>/dev/null || true
        _spinner_pid=""
    fi
    tput cnorm 2>/dev/null || true
    if [[ "$success" == "true" ]]; then
        printf "\r${GREEN}[✓]${NC} %s\n" "$label" >&2
    else
        printf "\r${RED}[✗]${NC} %s\n" "$label" >&2
    fi
}

# Run a command quietly with an animated spinner, or verbosely in debug mode
# Usage: run_quiet "Installing packages..." command arg1 arg2 ...
run_quiet() {
    local label="$1"
    shift

    if [[ "$DEBUG" == "true" ]]; then
        echo -e "${CYAN}[i]${NC} ${label}"
        "$@"
        return $?
    fi

    local log_file
    log_file=$(mktemp /tmp/jabali-install-XXXXXX.log)

    start_spinner "$label"

    local exit_code=0
    "$@" > "$log_file" 2>&1 || exit_code=$?

    if [[ $exit_code -eq 0 ]]; then
        stop_spinner true "$label"
    else
        stop_spinner false "$label"
        echo -e "${YELLOW}    Command failed. Last 20 lines of output:${NC}"
        tail -20 "$log_file" | sed 's/^/    /'
    fi

    rm -f "$log_file"
    return $exit_code
}

# Check if running as root
check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "This script must be run as root (use sudo)"
    fi
}

# Check OS
check_os() {
    if [[ ! -f /etc/debian_version ]]; then
        error "This installer only supports Debian/Ubuntu systems"
    fi

    . /etc/os-release
    info "Detected: $PRETTY_NAME"

    case $ID in
        debian)
            # Read debian_version file
            local debian_ver=$(cat /etc/debian_version 2>/dev/null || echo "")

            if [[ -n "${VERSION_ID:-}" ]]; then
                # Stable release with a numeric version
                if [[ "${VERSION_ID}" -ge 13 ]]; then
                    info "Detected Debian ${VERSION_ID} (${VERSION_CODENAME:-unknown})"
                else
                    error "Debian 13 or later is required"
                fi
            elif [[ "$debian_ver" == *sid* ]] || [[ "$VERSION_CODENAME" == "sid" ]]; then
                info "Detected Debian unstable (sid) - proceeding"
            else
                # Testing without VERSION_ID (e.g., next unreleased version)
                info "Detected Debian testing (${VERSION_CODENAME:-unknown}) - proceeding"
            fi
            ;;
        ubuntu)
            if [[ -n "${VERSION_ID:-}" ]] && [[ ${VERSION_ID%.*} -lt 22 ]]; then
                error "Ubuntu 22.04 or later is required"
            fi
            ;;
        *)
            warn "Untested distribution: $ID. Proceeding anyway..."
            ;;
    esac
}

# Display banner
show_banner() {
    echo ""
    echo -e "${YELLOW}░░░░░██╗░█████╗░██████╗░░█████╗░██╗░░░░░██╗${NC}"
    echo -e "${YELLOW}░░░░░██║██╔══██╗██╔══██╗██╔══██╗██║░░░░░██║${NC}"
    echo -e "${YELLOW}░░░░░██║███████║██████╦╝███████║██║░░░░░██║${NC}"
    echo -e "${YELLOW}██╗░░██║██╔══██║██╔══██╗██╔══██║██║░░░░░██║${NC}"
    echo -e "${YELLOW}╚█████╔╝██║░░██║██████╦╝██║░░██║███████╗██║${NC}"
    echo -e "${YELLOW}░╚════╝░╚═╝░░╚═╝╚═════╝░╚═╝░░╚═╝╚══════╝╚═╝${NC}"
    echo ""
    echo -e "  ${BOLD}Jabali Panel${NC} v${JABALI_VERSION} - ${CYAN}Modern Web Hosting Control Panel${NC}"
    echo ""
}

# Prompt for server hostname
prompt_hostname() {
    local current_hostname=$(hostname -f 2>/dev/null || hostname)
    local server_ip=$(hostname -I | awk '{print $1}')

    # Check if SERVER_HOSTNAME is already set (non-interactive mode)
    if [[ -n "$SERVER_HOSTNAME" ]]; then
        # Validate the preset hostname
        if [[ ! "$SERVER_HOSTNAME" =~ ^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$ ]]; then
            error "Invalid hostname format: $SERVER_HOSTNAME. Please use a valid FQDN (e.g., panel.example.com)"
        fi
        info "Using preset hostname: $SERVER_HOSTNAME"
    else
        echo -e "${BOLD}Server Configuration${NC}"
        echo ""
        echo "Enter the fully qualified domain name (FQDN) for this server."
        echo "This will be used for:"
        echo "  - Server hostname"
        echo "  - Admin email address (admin@hostname)"
        echo "  - Mail server configuration"
        echo ""
        echo -e "Current hostname: ${CYAN}${current_hostname}${NC}"
        echo -e "Server IP: ${CYAN}${server_ip}${NC}"
        echo ""

        while true; do
            read -p "Enter hostname [$current_hostname]: " SERVER_HOSTNAME < /dev/tty

            # Use current hostname as default if empty
            if [[ -z "$SERVER_HOSTNAME" ]]; then
                SERVER_HOSTNAME="$current_hostname"
            fi

            # Basic hostname validation
            if [[ ! "$SERVER_HOSTNAME" =~ ^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$ ]]; then
                warn "Invalid hostname format. Please use a valid FQDN (e.g., panel.example.com)"
                continue
            fi

            break
        done

        echo ""
        info "Using hostname: $SERVER_HOSTNAME"
        echo ""
    fi

    # Set system hostname
    hostnamectl set-hostname "$SERVER_HOSTNAME" 2>/dev/null || hostname "$SERVER_HOSTNAME"

    # Update /etc/hosts
    if ! grep -q "$SERVER_HOSTNAME" /etc/hosts; then
        echo "127.0.1.1 $SERVER_HOSTNAME" >> /etc/hosts
    fi

    # Export for use in other functions
    export SERVER_HOSTNAME
    export ADMIN_EMAIL="admin@${SERVER_HOSTNAME}"
}

# Add required repositories
add_repositories() {
    header "Adding Repositories"

    # Disable needrestart interactive prompts (hangs non-interactive installs)
    if [[ -d /etc/needrestart/conf.d ]]; then
        echo '$nrconf{restart} = "a";' > /etc/needrestart/conf.d/99-jabali.conf
    fi
    export NEEDRESTART_MODE=a
    export DEBIAN_FRONTEND=noninteractive

    # Fix any broken dpkg state from previous installs
    # Force-remove any half-installed packages that block dpkg
    dpkg -l 2>/dev/null | awk '/^.F/ || /^iU/ {print $2}' | while read -r pkg; do
        dpkg --remove --force-remove-reinstreq "$pkg" 2>/dev/null || true
    done
    DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-confold 2>/dev/null || true

    # Clean up stale third-party source files from previous installs (they cause apt-get update failures)
    rm -f /etc/apt/sources.list.d/php.list /etc/apt/sources.list.d/nginx.list

    # Update package list
    run_quiet "Updating package lists..." apt-get update -qq

    # Install prerequisites (software-properties-common is optional, mainly for Ubuntu)
    run_quiet "Installing prerequisites..." env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq apt-transport-https ca-certificates curl gnupg lsb-release sudo whois
    apt-get install -y -qq software-properties-common > /dev/null 2>&1 || true

    # Detect codename
    local codename=$(lsb_release -sc)

    # Ensure Debian contrib repository for geoipupdate and related packages
    # Only on actual Debian, not Ubuntu (which also has /etc/debian_version)
    if [[ -f /etc/debian_version ]] && ! grep -qi ubuntu /etc/os-release 2>/dev/null; then
        info "Ensuring Debian contrib repository..."
        local contrib_list="/etc/apt/sources.list.d/jabali-contrib.list"
        if [[ ! -f "$contrib_list" ]]; then
            cat > "$contrib_list" <<EOF
deb https://deb.debian.org/debian ${codename} contrib
deb https://deb.debian.org/debian ${codename}-updates contrib
deb https://security.debian.org/debian-security ${codename}-security contrib
EOF
        fi
    fi

    # Add PHP repository
    info "Configuring PHP repository..."
    if grep -qi ubuntu /etc/os-release 2>/dev/null; then
        # Ubuntu: use Ondrej PPA via Launchpad (avoids Sury CDN rate limiting)
        info "Using Ondrej PHP PPA for Ubuntu ($codename)"
        if ! add-apt-repository -y ppa:ondrej/php 2>/dev/null; then
            # Fallback: direct Sury URL
            warn "PPA failed, trying Sury direct URL..."
            curl -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor --yes -o /usr/share/keyrings/sury-php.gpg 2>/dev/null || true
            echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ubuntu/ ${codename} main" > /etc/apt/sources.list.d/php.list
        fi
    else
        # Debian: use Sury's Debian repository
        info "Using Sury PHP repository for Debian ($codename)"
        curl -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor --yes -o /usr/share/keyrings/sury-php.gpg 2>/dev/null || true
        echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${codename} main" > /etc/apt/sources.list.d/php.list
    fi

    # Add nginx repository (recommended when using sury PHP)
    info "Configuring nginx repository..."
    if grep -qi ubuntu /etc/os-release 2>/dev/null; then
        add-apt-repository -y ppa:ondrej/nginx 2>/dev/null || warn "Ondrej nginx PPA unavailable, using distro nginx"
    else
        curl -fsSL https://packages.sury.org/nginx/apt.gpg | gpg --dearmor --yes -o /usr/share/keyrings/sury-nginx.gpg 2>/dev/null || true
        echo "deb [signed-by=/usr/share/keyrings/sury-nginx.gpg] https://packages.sury.org/nginx/ ${codename} main" > /etc/apt/sources.list.d/nginx.list
    fi

    # Add NodeJS repository
    if [[ ! -f /usr/share/keyrings/nodesource.gpg ]]; then
        info "Adding NodeJS repository..."
        curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/nodesource.gpg 2>/dev/null || true
        echo "deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_VERSION}.x nodistro main" > /etc/apt/sources.list.d/nodesource.list
        # Clean up DEB822 format file if it exists (from previous setup script)
        rm -f /etc/apt/sources.list.d/nodesource.sources
    fi

    # Add MariaDB repository (optional, system version usually fine)

    run_quiet "Updating package lists..." apt-get update -qq
    log "Repositories configured"
}

# Install system packages
install_packages() {
    header "Installing System Packages"

    # Clean up conflicting packages from previous failed installations
    if dpkg -l apache2 2>/dev/null | grep -q '^ii'; then
        info "Removing Apache2 (conflicts with nginx)..."
        systemctl stop apache2 2>/dev/null || true
        systemctl disable apache2 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get purge -y apache2 apache2-bin apache2-utils apache2-data 2>/dev/null || true
    fi

    # Clean up broken PHP state from previous failed installations
    # Detect which PHP version packages are present (if any) for cleanup
    local _cleanup_ver=""
    for _cv in 8.5 8.4 8.3 8.2 8.1; do
        if dpkg -l "php${_cv}*" 2>/dev/null | grep -qE '^(ii|rc|iU|iF|hi|pi)'; then
            _cleanup_ver="$_cv"
            break
        fi
    done
    if [[ -n "$_cleanup_ver" ]]; then
        # Check if configs are missing or broken
        if [[ ! -f "/etc/php/${_cleanup_ver}/fpm/php.ini" ]] || [[ ! -f "/etc/php/${_cleanup_ver}/cli/php.ini" ]] || \
           dpkg -l "php${_cleanup_ver}-fpm" 2>/dev/null | grep -qE '^(rc|iU|iF)'; then
            info "Cleaning up broken PHP ${_cleanup_ver} installation..."
            systemctl stop "php${_cleanup_ver}-fpm" 2>/dev/null || true
            systemctl reset-failed "php${_cleanup_ver}-fpm" 2>/dev/null || true
            DEBIAN_FRONTEND=noninteractive apt-get purge -y "php${_cleanup_ver}*" 2>/dev/null || true
            rm -rf "/etc/php/${_cleanup_ver}"
            apt-get clean
            dpkg --configure -a 2>/dev/null || true
        fi
    fi

    # Core packages (always installed)
    local base_packages=(
        # Web Server
        nginx

        # Database
        mariadb-server
        mariadb-client

        # Cache
        redis-server

        # SSL
        ssl-cert
        certbot
        python3-certbot-nginx

        # Utilities
        git
        curl
        wget
        zip
        unzip
        cron
        htop
        net-tools
        dnsutils
        nodejs
        acl
        socat
        sshpass
        pigz
        restic
        locales

        # GeoIP (always installed)
        geoipupdate
        libnginx-mod-http-geoip2

        # For screenshots (Puppeteer)
        chromium

        # For PHP compilation/extensions
        build-essential

        # Disk quota management
        quota

        # Log analysis
        goaccess

    )

    # Add Mail Server packages if enabled
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        info "Including Stalwart Mail Server dependencies..."
        # Stalwart is a single binary downloaded separately — only need imapsync deps here
        # IMAP sync dependencies
        base_packages+=(
            libmail-imapclient-perl
            libio-tee-perl
            libterm-readkey-perl
            libfile-copy-recursive-perl
            libunicode-string-perl
            libauthen-ntlm-perl
            libcgi-pm-perl
            libcrypt-openssl-rsa-perl
            libdata-uniqid-perl
            libencode-imaputf7-perl
            libio-socket-inet6-perl
            libio-socket-ssl-perl
            libjson-webtoken-perl
            libmodule-scandeps-perl
            libreadonly-perl
            libregexp-common-perl
            libsys-meminfo-perl
            libfile-tail-perl
            # SQLite tools
            sqlite3
        )
    fi

    # Add DNS Server packages if enabled
    if [[ "$INSTALL_DNS" == "true" ]]; then
        info "Including DNS Server packages..."
        base_packages+=(
            pdns-server
            pdns-backend-mysql
        )
    fi

    # Prevent Apache2 and libapache2-mod-php from being installed
    # (php metapackage recommends libapache2-mod-php, but we use nginx+php-fpm)
    info "Blocking Apache2 and mod-php installation (we use nginx + php-fpm)..."
    apt-mark hold apache2 libapache2-mod-php libapache2-mod-php${PHP_VERSION:-8.5} 2>/dev/null || true

    run_quiet "Installing base packages (this may take a few minutes)..." \
        env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${base_packages[@]}" || {
        warn "Some packages may not be available, retrying individually..."
        for pkg in "${base_packages[@]}"; do
            run_quiet "  Installing $pkg..." apt-get install -y -qq --no-install-recommends "$pkg" || warn "Could not install: $pkg"
        done
    }

    # Remove conflicting MTAs — they grab port 25 before Stalwart can bind
    # Proxmox LXC templates ship Postfix; Debian base installs exim4
    for mta_pkg in postfix exim4-base; do
        if dpkg -l "$mta_pkg" >/dev/null 2>&1; then
            local mta_name="${mta_pkg%%-*}"
            run_quiet "Removing ${mta_name} (conflicts with Stalwart on port 25)..." \
                env DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq "${mta_name}*"
        fi
    done
    apt-get autoremove -y -qq >/dev/null 2>&1

    # Install imapsync binary (not in Debian repos)
    if [[ "$INSTALL_MAIL" == "true" ]] && ! command -v imapsync >/dev/null 2>&1; then
        info "Installing imapsync..."
        curl -fsSL https://raw.githubusercontent.com/imapsync/imapsync/master/imapsync -o /usr/local/bin/imapsync
        chmod +x /usr/local/bin/imapsync
        log "imapsync $(imapsync --version 2>&1 | head -1) installed"
    fi

    if command -v locale-gen >/dev/null 2>&1; then
        info "Configuring locales..."
        if [[ ! -f /etc/locale.gen ]]; then
            touch /etc/locale.gen
        fi
        if ! grep -q '^en_US.UTF-8 UTF-8' /etc/locale.gen 2>/dev/null; then
            echo "en_US.UTF-8 UTF-8" >> /etc/locale.gen
        fi
        if ! grep -q '^C.UTF-8 UTF-8' /etc/locale.gen 2>/dev/null; then
            echo "C.UTF-8 UTF-8" >> /etc/locale.gen
        fi
        locale-gen >/dev/null 2>&1 || warn "Locale generation failed"
        update-locale LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 >/dev/null 2>&1 || warn "Failed to set default locale"
        if [[ ! -f /etc/default/locale ]]; then
            touch /etc/default/locale
        fi
        if ! grep -q '^LANG=' /etc/default/locale 2>/dev/null; then
            echo "LANG=en_US.UTF-8" >> /etc/default/locale
        fi
        if ! grep -q '^LC_ALL=' /etc/default/locale 2>/dev/null; then
            echo "LC_ALL=en_US.UTF-8" >> /etc/default/locale
        fi
    fi

    # Unhold packages in case user wants to install them manually later
    apt-mark unhold apache2 libapache2-mod-php libapache2-mod-php${PHP_VERSION:-8.5} >/dev/null 2>&1 || true

    # Install PHP and detect the version from the Sury repository
    info "Installing PHP..."

    # Always target PHP 8.5 from Sury — do not fall back to distro default (8.4)
    PHP_VERSION="8.5"
    run_quiet "Installing PHP ${PHP_VERSION} base packages..." \
        env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-cli" 2>/dev/null || true
    if ! command -v "php${PHP_VERSION}" &>/dev/null; then
        warn "PHP ${PHP_VERSION} not available from Sury, falling back to distro default..."
        run_quiet "Installing distro PHP..." \
            env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends php-fpm php-cli 2>/dev/null || true
        if command -v php &>/dev/null; then
            PHP_VERSION=$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;' 2>/dev/null)
        fi
        PHP_VERSION="${PHP_VERSION:-8.4}"
    fi
    info "Using PHP ${PHP_VERSION}..."

    # Stop, disable and mask Apache2 if installed (conflicts with nginx on port 80)
    # Apache2 can be installed as a dependency of some PHP packages
    if dpkg -l apache2 2>/dev/null | grep -q '^ii' || systemctl is-active --quiet apache2 2>/dev/null; then
        info "Stopping Apache2 (conflicts with nginx)..."
        systemctl stop apache2 2>/dev/null || true
        systemctl disable apache2 2>/dev/null || true
        systemctl mask apache2 2>/dev/null || true
    fi

    # Clean up any broken PHP-FPM state from previous installations
    if systemctl is-failed --quiet php${PHP_VERSION}-fpm 2>/dev/null; then
        info "Resetting failed PHP-FPM service state..."
        systemctl reset-failed php${PHP_VERSION}-fpm
    fi

    # Fix any broken dpkg state before PHP install
    dpkg --configure -a 2>/dev/null || true

    # Required PHP extensions for Jabali Panel
    local php_extensions=(
        php${PHP_VERSION}
        php${PHP_VERSION}-fpm
        php${PHP_VERSION}-cli
        php${PHP_VERSION}-common
        php${PHP_VERSION}-mysql
        php${PHP_VERSION}-pgsql
        php${PHP_VERSION}-sqlite3
        php${PHP_VERSION}-curl
        php${PHP_VERSION}-gd
        php${PHP_VERSION}-mbstring
        php${PHP_VERSION}-xml          # Provides dom extension
        php${PHP_VERSION}-zip
        php${PHP_VERSION}-bcmath
        php${PHP_VERSION}-intl         # Required by Filament
        php${PHP_VERSION}-readline
        php${PHP_VERSION}-soap
        php${PHP_VERSION}-imap
        php${PHP_VERSION}-ldap
        php${PHP_VERSION}-imagick
        php${PHP_VERSION}-redis
        php${PHP_VERSION}-opcache
    )

    # Filter out packages that don't exist (e.g. opcache is bundled in php-common on some versions)
    local available_extensions=()
    for pkg in "${php_extensions[@]}"; do
        if apt-cache show "$pkg" &>/dev/null; then
            available_extensions+=("$pkg")
        else
            info "Skipping $pkg (bundled or unavailable)"
        fi
    done

    # Install all PHP packages (use --force-confmiss to handle dpkg's "deleted config" state)
    if ! run_quiet "Installing PHP extensions..." \
        env DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" "${available_extensions[@]}"; then
        warn "PHP installation had errors, attempting aggressive recovery..."

        # Stop PHP-FPM if it's somehow running in a broken state
        systemctl stop php${PHP_VERSION}-fpm 2>/dev/null || true
        systemctl reset-failed php${PHP_VERSION}-fpm 2>/dev/null || true

        # Purge ALL PHP ${PHP_VERSION} packages including config files
        info "Purging all PHP ${PHP_VERSION} packages..."
        DEBIAN_FRONTEND=noninteractive apt-get purge -y "php${PHP_VERSION}*" 2>/dev/null || true

        # Also remove libapache2-mod-php if it got installed (it conflicts with php-fpm)
        DEBIAN_FRONTEND=noninteractive apt-get purge -y 'libapache2-mod-php*' 2>/dev/null || true

        # Remove config directories to force fresh install (dpkg won't replace "deleted" configs)
        info "Removing PHP config directories..."
        rm -rf /etc/php/${PHP_VERSION}/fpm
        rm -rf /etc/php/${PHP_VERSION}/cli
        rm -rf /etc/php/${PHP_VERSION}/apache2

        # Clean package cache
        apt-get clean
        apt-get autoclean

        # Fix any broken dpkg state
        dpkg --configure -a 2>/dev/null || true

        # Reinstall PHP with force-confmiss to ensure config files are created
        if ! run_quiet "Reinstalling PHP ${PHP_VERSION} with fresh configuration..." \
            env DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" "${php_extensions[@]}"; then
            error "Failed to install PHP ${PHP_VERSION}. Please check your system's package state and try again."
        fi
    fi

    # Stop and disable Apache2 completely - it conflicts with nginx on port 80
    # Apache2 can be installed as a dependency of PHP packages
    if dpkg -l apache2 2>/dev/null | grep -q '^ii'; then
        info "Disabling Apache2 (conflicts with nginx)..."
        systemctl stop apache2 2>/dev/null || true
        systemctl disable apache2 2>/dev/null || true
        systemctl mask apache2 2>/dev/null || true  # Prevent it from starting
    fi

    # Verify PHP is installed and working
    if ! php -v 2>/dev/null | grep -q "PHP ${PHP_VERSION}"; then
        error "PHP ${PHP_VERSION} installation failed. Found: $(php -v 2>/dev/null | head -1)"
    fi

    log "PHP ${PHP_VERSION} installed successfully"

    # Ensure PHP-FPM is properly configured
    if [[ ! -f "/etc/php/${PHP_VERSION}/fpm/php-fpm.conf" ]] || [[ ! -f "/etc/php/${PHP_VERSION}/fpm/php.ini" ]]; then
        warn "PHP-FPM config files missing after install"
        info "Purging and reinstalling PHP-FPM with fresh config..."
        systemctl stop php${PHP_VERSION}-fpm 2>/dev/null || true
        systemctl reset-failed php${PHP_VERSION}-fpm 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php${PHP_VERSION}-fpm > /dev/null 2>&1 || true
        rm -rf /etc/php/${PHP_VERSION}/fpm
        apt-get clean > /dev/null 2>&1
        run_quiet "Reinstalling PHP-FPM..." \
            env DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" php${PHP_VERSION}-fpm
    fi

    # Verify PHP-FPM is running
    if ! systemctl is-active --quiet php${PHP_VERSION}-fpm; then
        # Reset failed state first if needed
        systemctl reset-failed php${PHP_VERSION}-fpm 2>/dev/null || true
        if ! systemctl start php${PHP_VERSION}-fpm; then
            warn "PHP-FPM failed to start, attempting recovery..."
            # Check for config errors
            php-fpm${PHP_VERSION} -t 2>&1 || true
            systemctl status php${PHP_VERSION}-fpm --no-pager -l || true
        fi
    fi

    # Panel is now served by FrankenPHP — PHP-FPM panel pool is no longer needed
    if false; then
    # Create dedicated PHP-FPM service for the panel to avoid interrupting user pools
    local panel_fpm_dir="/etc/php/${PHP_VERSION}/fpm-panel"
    mkdir -p "${panel_fpm_dir}/pool.d"

    cat > "${panel_fpm_dir}/php-fpm.conf" <<EOF
[global]
pid = /run/php/php${PHP_VERSION}-fpm-panel.pid
error_log = /var/log/php${PHP_VERSION}-fpm-panel.log
include=/etc/php/${PHP_VERSION}/fpm-panel/pool.d/*.conf
EOF

    cat > "${panel_fpm_dir}/pool.d/panel.conf" <<EOF
[panel]
user = www-data
group = www-data
listen = /run/php/php${PHP_VERSION}-fpm-panel.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = dynamic
pm.max_children = 10
pm.start_servers = 2
pm.min_spare_servers = 2
pm.max_spare_servers = 4
catch_workers_output = yes
php_admin_flag[log_errors] = on
php_admin_value[error_log] = /var/log/php${PHP_VERSION}-fpm-panel.pool.log
chdir = /
EOF

    cat > /etc/systemd/system/php${PHP_VERSION}-fpm-panel.service <<EOF
[Unit]
Description=The PHP ${PHP_VERSION} FastCGI Process Manager (Panel)
Documentation=man:php-fpm${PHP_VERSION}(8)
After=network.target

[Service]
Type=notify
ExecStart=/usr/sbin/php-fpm${PHP_VERSION} --nodaemonize --fpm-config /etc/php/${PHP_VERSION}/fpm-panel/php-fpm.conf
ExecReload=/bin/kill -USR2 \$MAINPID
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now php${PHP_VERSION}-fpm-panel
    fi

    # Verify PHP CLI is working and has required extensions
    info "Verifying PHP CLI and extensions..."

    if ! command -v php &>/dev/null; then
        error "PHP CLI is not in PATH after installation."
    fi

    # Ensure php.ini exists for CLI (dpkg doesn't replace deleted config files)
    local cli_ini="/etc/php/${PHP_VERSION}/cli/php.ini"

    if [[ ! -f "$cli_ini" ]]; then
        warn "PHP CLI config file missing: $cli_ini"
        info "Reinstalling php${PHP_VERSION}-cli with fresh config..."
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php${PHP_VERSION}-cli > /dev/null 2>&1 || true
        rm -rf /etc/php/${PHP_VERSION}/cli
        run_quiet "Reinstalling PHP CLI..." \
            env DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" php${PHP_VERSION}-cli
    fi

    # Verify required extensions are available
    local missing_ext=""
    php -r "class_exists('Phar') || exit(1);" 2>/dev/null || missing_ext="$missing_ext phar"
    php -r "extension_loaded('dom') || exit(1);" 2>/dev/null || missing_ext="$missing_ext dom"
    php -r "extension_loaded('intl') || exit(1);" 2>/dev/null || missing_ext="$missing_ext intl"
    php -r "extension_loaded('mbstring') || exit(1);" 2>/dev/null || missing_ext="$missing_ext mbstring"

    if [[ -n "$missing_ext" ]]; then
        warn "Missing PHP extensions:$missing_ext"
        info "Extension .ini files may be missing. Purging and reinstalling PHP packages..."

        # Purge php-common to remove stale config state, then reinstall all packages
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php${PHP_VERSION}-common > /dev/null 2>&1
        run_quiet "Reinstalling PHP extensions..." \
            env DEBIAN_FRONTEND=noninteractive apt-get install -y "${php_extensions[@]}"

        # Check again
        php -r "class_exists('Phar') || exit(1);" 2>/dev/null || error "PHP Phar extension is missing"
        php -r "extension_loaded('dom') || exit(1);" 2>/dev/null || error "PHP DOM extension is missing (install php${PHP_VERSION}-xml)"
        php -r "extension_loaded('intl') || exit(1);" 2>/dev/null || error "PHP Intl extension is missing (install php${PHP_VERSION}-intl)"
    fi

    log "PHP ${PHP_VERSION} CLI verified with all required extensions"

    # Final Apache2 cleanup - ensure it's stopped and masked before nginx starts
    if systemctl is-active --quiet apache2 2>/dev/null; then
        warn "Apache2 is still running, forcing stop..."
        systemctl stop apache2 || true
        systemctl disable apache2 || true
        systemctl mask apache2 || true
    fi

    log "System packages installed"
}

install_geoipupdate_binary() {
    if command -v geoipupdate &>/dev/null; then
        return
    fi

    info "geoipupdate not found, installing from MaxMind releases..."

    local arch
    arch="$(uname -m)"
    local arch_token="$arch"
    if [[ "$arch" == "x86_64" ]]; then
        arch_token="amd64"
    elif [[ "$arch" == "aarch64" || "$arch" == "arm64" ]]; then
        arch_token="arm64"
    fi

    local api_url="https://api.github.com/repos/maxmind/geoipupdate/releases/latest"
    local metadata
    metadata=$(curl -fsSL "$api_url" 2>/dev/null || true)
    if [[ -z "$metadata" ]]; then
        metadata=$(wget -qO- "$api_url" 2>/dev/null || true)
    fi

    if [[ -z "$metadata" ]]; then
        warn "Failed to download geoipupdate release metadata"
        return
    fi

    local download_url
    download_url=$(echo "$metadata" | grep -Eo "https://[^\"]+${arch_token}[^\"]+\\.tar\\.gz" | head -n1)
    if [[ -z "$download_url" && "$arch_token" == "amd64" ]]; then
        download_url=$(echo "$metadata" | grep -Eo "https://[^\"]+x86_64[^\"]+\\.tar\\.gz" | head -n1)
    fi

    if [[ -z "$download_url" ]]; then
        warn "No suitable geoipupdate binary found for ${arch}"
        return
    fi

    local tmp_dir
    tmp_dir=$(mktemp -d)
    local archive="${tmp_dir}/geoipupdate.tgz"

    if command -v curl &>/dev/null; then
        curl -fsSL "$download_url" -o "$archive" 2>/dev/null || true
    else
        wget -qO "$archive" "$download_url" 2>/dev/null || true
    fi

    if [[ ! -s "$archive" ]]; then
        warn "Failed to download geoipupdate binary"
        rm -rf "$tmp_dir"
        return
    fi

    tar -xzf "$archive" -C "$tmp_dir" 2>/dev/null || true
    local binary
    binary=$(find "$tmp_dir" -type f -name geoipupdate | head -n1)
    if [[ -z "$binary" ]]; then
        warn "geoipupdate binary not found in archive"
        rm -rf "$tmp_dir"
        return
    fi

    install -m 0755 "$binary" /usr/local/bin/geoipupdate 2>/dev/null || true
    rm -rf "$tmp_dir"
}

# Install Composer
install_composer() {
    header "Installing Composer"

    # PHP and Phar extension should already be verified by install_packages
    # Just do a quick sanity check
    if ! command -v php &>/dev/null; then
        error "PHP is not installed or not in PATH"
    fi

    # Quick Phar check (should already be verified, but be safe)
    if ! php -r "echo class_exists('Phar') ? 'ok' : 'no';" 2>/dev/null | grep -q ok; then
        error "PHP Phar extension is required. Please reinstall PHP CLI: apt-get install --reinstall php${PHP_VERSION}-cli"
    fi

    info "PHP Phar extension: OK"

    if command -v composer &>/dev/null; then
        log "Composer already installed"
        return
    fi

    info "Downloading and installing Composer..."

    local max_retries=3
    local retry_delay=5
    local attempt

    for attempt in $(seq 1 $max_retries); do
        if curl -sS --retry 3 --retry-delay 3 --connect-timeout 30 --max-time 120 \
            https://getcomposer.org/installer -o /tmp/composer-setup.php 2>/dev/null; then
            if php /tmp/composer-setup.php --install-dir=/usr/local/bin --filename=composer 2>/dev/null; then
                rm -f /tmp/composer-setup.php
                break
            fi
        fi

        if [ "$attempt" -lt "$max_retries" ]; then
            warn "Composer download failed (attempt $attempt/$max_retries), retrying in ${retry_delay}s..."
            sleep $retry_delay
            retry_delay=$((retry_delay * 2))
        fi
    done

    rm -f /tmp/composer-setup.php

    if ! command -v composer &>/dev/null; then
        error "Composer installation failed after $max_retries attempts. Check your network connection and try again."
    fi

    log "Composer installed"
}

# Install FrankenPHP
FRANKENPHP_VERSION="1.12.1"

install_frankenphp() {
    header "Installing FrankenPHP"

    if [ -f /usr/local/bin/frankenphp ]; then
        local current_ver
        current_ver=$(/usr/local/bin/frankenphp version 2>/dev/null | grep -oP 'FrankenPHP v\K[0-9.]+' | head -1 || echo "unknown")
        if [[ "$current_ver" == "$FRANKENPHP_VERSION" ]]; then
            log "FrankenPHP v${FRANKENPHP_VERSION} already installed"
            return
        fi
        info "Upgrading FrankenPHP from v${current_ver} to v${FRANKENPHP_VERSION}..."

        # Stop the panel service before replacing the binary (prevents "text file busy")
        systemctl stop jabali-panel 2>/dev/null || true
    fi

    local arch
    case "$(uname -m)" in
        x86_64)  arch="x86_64" ;;
        aarch64) arch="aarch64" ;;
        arm64)   arch="aarch64" ;;
        *)       error "Unsupported architecture: $(uname -m)" ;;
    esac

    info "Downloading FrankenPHP v${FRANKENPHP_VERSION} for ${arch}..."
    curl -fsSL --retry 3 --retry-delay 3 --connect-timeout 30 --max-time 300 \
        "https://github.com/dunglas/frankenphp/releases/download/v${FRANKENPHP_VERSION}/frankenphp-linux-${arch}" \
        -o /usr/local/bin/frankenphp

    chmod 755 /usr/local/bin/frankenphp

    if ! /usr/local/bin/frankenphp version &>/dev/null; then
        error "FrankenPHP binary verification failed"
    fi

    log "FrankenPHP installed"
}

# Configure FrankenPHP Caddyfile
setup_frankenphp_config() {
    header "Configuring FrankenPHP"

    mkdir -p /etc/jabali /etc/jabali/agent.d /etc/frankenphp
    mkdir -p /var/lib/jabali/caddy
    chown www-data:www-data /var/lib/jabali/caddy

    # FrankenPHP php.ini — match the system PHP settings
    cat > /etc/frankenphp/php.ini <<'PHPINI'
[PHP]
max_execution_time = 600
max_input_time = 600
memory_limit = 512M
post_max_size = 512M
upload_max_filesize = 512M
max_file_uploads = 50
date.timezone = UTC

[opcache]
opcache.enable=1
opcache.memory_consumption=128
opcache.interned_strings_buffer=16
opcache.max_accelerated_files=20000
opcache.validate_timestamps=0
opcache.save_comments=1
opcache.preload=/var/www/jabali/bootstrap/preload.php
opcache.preload_user=www-data

[realpath_cache]
realpath_cache_size=4096K
realpath_cache_ttl=600
PHPINI

    local panel_hostname="${SERVER_HOSTNAME:-$(hostname -f 2>/dev/null || hostname)}"
    local acme_email="${ADMIN_EMAIL:-admin@${panel_hostname}}"

    # Generate self-signed cert for panel if it doesn't exist
    local ssl_dir="/etc/ssl/jabali"
    if [[ ! -f "$ssl_dir/panel.crt" ]] || [[ ! -f "$ssl_dir/panel.key" ]]; then
        mkdir -p "$ssl_dir"
        openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
            -keyout "$ssl_dir/panel.key" \
            -out "$ssl_dir/panel.crt" \
            -subj "/CN=${panel_hostname}" \
            -addext "subjectAltName=DNS:${panel_hostname}" 2>/dev/null
        chown root:www-data "$ssl_dir/panel.key"
        chmod 640 "$ssl_dir/panel.key"
        log "Generated self-signed SSL certificate for panel"
    fi

    local cpus
    cpus=$(nproc 2>/dev/null || echo 2)
    local num_threads=$(( cpus * 2 ))
    [ "$num_threads" -lt 2 ] && num_threads=2
    [ "$num_threads" -gt 8 ] && num_threads=8

    cat > /etc/jabali/Caddyfile <<CADDYEOF
{
	frankenphp {
		num_threads ${num_threads}
		max_threads $(( num_threads * 3 ))
	}
	order php_server before file_server

	admin off
	log {
		level WARN
	}

	# Disable automatic HTTPS/ACME — panel uses self-signed or certbot-managed certs
	auto_https off

	servers {
		protocols h1 h2 h3
	}
}

:${PANEL_PORT} {
	root * /var/www/jabali/public

	tls /etc/ssl/jabali/panel.crt /etc/ssl/jabali/panel.key

	encode zstd gzip

	# Performance: don't resolve symlinks on every request
	php_server {
		resolve_root_symlink false
		try_files {path} index.php
	}

	header {
		X-Frame-Options "SAMEORIGIN"
		X-Content-Type-Options "nosniff"
		X-XSS-Protection "1; mode=block"
		Strict-Transport-Security "max-age=0"
		Referrer-Policy "strict-origin-when-cross-origin"
	}

	request_body {
		max_size 512MB
	}

	@blocked {
		path /vendor/*
		not path /vendor/livewire/*
	}
	respond @blocked 404

	@blocked_other {
		path /node_modules/* /.*
		not path /.well-known/*
	}
	respond @blocked_other 404
}
CADDYEOF

    log "FrankenPHP configured"
}

# Setup FrankenPHP panel systemd service
setup_panel_service() {
    header "Setting up Panel Service"

    cat > /etc/systemd/system/jabali-panel.service <<'SVCEOF'
[Unit]
Description=Jabali Panel (FrankenPHP)
After=network.target mariadb.service redis-server.service
Wants=mariadb.service redis-server.service

[Service]
Type=simple
User=www-data
Group=www-data
WorkingDirectory=/var/www/jabali
Environment=XDG_DATA_HOME=/var/lib/jabali
Environment=GODEBUG=cgocheck=0
Environment=GOMEMLIMIT=512MiB
ExecStart=/usr/local/bin/frankenphp run --config /etc/jabali/Caddyfile
ExecReload=/usr/local/bin/frankenphp reload --config /etc/jabali/Caddyfile
TimeoutStopSec=10
KillMode=mixed
Restart=always
RestartSec=5
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jabali-panel

[Install]
WantedBy=multi-user.target
SVCEOF

    systemctl daemon-reload
    systemctl enable jabali-panel
    systemctl start jabali-panel

    log "Panel service started"
}

# Install WP-CLI
install_wp_cli() {
    header "Installing WP-CLI"

    if command -v wp &>/dev/null; then
        log "WP-CLI already installed"
        return
    fi

    local wp_url="https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar"
    local wp_path="/usr/local/bin/wp"

    if ! curl -fsSL "$wp_url" -o "$wp_path"; then
        warn "Failed to download WP-CLI"
        return
    fi

    chmod +x "$wp_path"

    if ! command -v wp &>/dev/null; then
        warn "WP-CLI installed but not found on PATH"
        return
    fi

    log "WP-CLI installed successfully"
}

# Install phpMyAdmin with Jabali SSO signon
install_phpmyadmin() {
    header "Installing phpMyAdmin"

    # Pre-configure debconf to skip auto-configure for any web server
    echo "phpmyadmin phpmyadmin/dbconfig-install boolean false" | debconf-set-selections 2>/dev/null || true
    echo "phpmyadmin phpmyadmin/reconfigure-webserver multiselect" | debconf-set-selections 2>/dev/null || true

    run_quiet "Installing phpMyAdmin package..." \
        env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq phpmyadmin

    # Copy Jabali signon script
    if [[ -f "$JABALI_DIR/stubs/phpmyadmin/jabali-signon.php" ]]; then
        cp "$JABALI_DIR/stubs/phpmyadmin/jabali-signon.php" /usr/share/phpmyadmin/jabali-signon.php
        chown root:www-data /usr/share/phpmyadmin/jabali-signon.php
        chmod 644 /usr/share/phpmyadmin/jabali-signon.php
    else
        warn "phpMyAdmin signon script not found in stubs"
    fi

    # Copy Jabali config to phpMyAdmin conf.d
    mkdir -p /etc/phpmyadmin/conf.d
    if [[ -f "$JABALI_DIR/stubs/phpmyadmin/config.inc.php" ]]; then
        cp "$JABALI_DIR/stubs/phpmyadmin/config.inc.php" /etc/phpmyadmin/conf.d/jabali.inc.php

        # Generate random blowfish secret
        local blowfish_secret
        blowfish_secret=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)
        sed -i "s|%%BLOWFISH_SECRET%%|${blowfish_secret}|g" /etc/phpmyadmin/conf.d/jabali.inc.php

        chown root:www-data /etc/phpmyadmin/conf.d/jabali.inc.php
        chmod 640 /etc/phpmyadmin/conf.d/jabali.inc.php
    else
        warn "phpMyAdmin config stub not found"
    fi

    log "phpMyAdmin installed with Jabali SSO signon"
}

# Clone Jabali Panel
clone_jabali() {
    header "Installing Jabali Panel"

    if [[ -d "$JABALI_DIR" ]]; then
        warn "Jabali directory exists, backing up..."
        mv "$JABALI_DIR" "${JABALI_DIR}.bak.$(date +%s)"
    fi

    run_quiet "Cloning Jabali Panel repository..." git clone -b "$JABALI_BRANCH" "$JABALI_REPO" "$JABALI_DIR"
    chown -R $JABALI_USER:$JABALI_USER "$JABALI_DIR"

    # Prevent git safe.directory issues for upgrades run as root or www-data
    git config --system --add safe.directory "$JABALI_DIR" 2>/dev/null || true
    sudo -u $JABALI_USER git config --global --add safe.directory "$JABALI_DIR" 2>/dev/null || true

    # Ensure runtime directories stay writable for PHP-FPM (default: www-data)
    if id www-data &>/dev/null; then
        chown -R $JABALI_USER:www-data \
            "$JABALI_DIR/database" \
            "$JABALI_DIR/storage" \
            "$JABALI_DIR/bootstrap/cache" 2>/dev/null || true
        chmod -R g+rwX \
            "$JABALI_DIR/database" \
            "$JABALI_DIR/storage" \
            "$JABALI_DIR/bootstrap/cache" 2>/dev/null || true
        find "$JABALI_DIR/database" "$JABALI_DIR/storage" "$JABALI_DIR/bootstrap/cache" -type d -exec chmod g+s {} + 2>/dev/null || true
    fi

    # Read version from cloned VERSION file
    if [[ -f "$JABALI_DIR/VERSION" ]]; then
        source "$JABALI_DIR/VERSION"
        JABALI_VERSION="${VERSION:-$JABALI_VERSION}"
        info "Installed version: ${JABALI_VERSION}"
    fi

    log "Jabali Panel cloned"
}

# Configure PHP
configure_php() {
    header "Configuring PHP"

    # Detect PHP version if not already set
    if [[ -z "$PHP_VERSION" ]]; then
        detect_php_version
    fi

    # Start PHP-FPM first to ensure files are created
    if ! systemctl start php${PHP_VERSION}-fpm 2>/dev/null; then
        error "Failed to start PHP-FPM ${PHP_VERSION}. Check: journalctl -u php${PHP_VERSION}-fpm"
    fi

    # Find PHP ini file - check multiple locations
    local php_ini=""
    local possible_paths=(
        "/etc/php/${PHP_VERSION}/fpm/php.ini"
        "/etc/php/${PHP_VERSION}/cli/php.ini"
        "/etc/php/${PHP_VERSION}/php.ini"
        "/etc/php/php.ini"
    )

    for path in "${possible_paths[@]}"; do
        if [[ -f "$path" ]]; then
            php_ini="$path"
            break
        fi
    done

    # If still not found, try to find it with broader search
    if [[ -z "$php_ini" ]]; then
        php_ini=$(find /etc/php -name "php.ini" 2>/dev/null | head -1)
    fi

    if [[ -z "$php_ini" || ! -f "$php_ini" ]]; then
        warn "PHP configuration not found, skipping PHP configuration"
        warn "You may need to configure PHP manually"
        return
    fi

    info "Configuring PHP ${PHP_VERSION} using $php_ini..."

    # PHP.ini settings for both FPM and CLI
    local ini_files=("$php_ini")
    [[ -f "/etc/php/${PHP_VERSION}/cli/php.ini" ]] && ini_files+=("/etc/php/${PHP_VERSION}/cli/php.ini")
    [[ -f "/etc/php/${PHP_VERSION}/fpm/php.ini" ]] && ini_files+=("/etc/php/${PHP_VERSION}/fpm/php.ini")

    # Remove duplicates
    ini_files=($(echo "${ini_files[@]}" | tr ' ' '\n' | sort -u | tr '\n' ' '))

    for ini in "${ini_files[@]}"; do
        if [[ -f "$ini" ]]; then
            sed -i 's/upload_max_filesize = .*/upload_max_filesize = 512M/' "$ini"
            sed -i 's/post_max_size = .*/post_max_size = 512M/' "$ini"
            sed -i 's/memory_limit = .*/memory_limit = 512M/' "$ini"
            sed -i 's/max_execution_time = .*/max_execution_time = 600/' "$ini"
            sed -i 's/max_input_time = .*/max_input_time = 600/' "$ini"
            sed -i 's/;date.timezone =.*/date.timezone = UTC/' "$ini"
        fi
    done

    # Enable necessary extensions
    phpenmod -v ${PHP_VERSION} phar curl mbstring xml zip 2>/dev/null || true

    # Configure PHP-FPM www pool for large uploads
    local www_pool="/etc/php/${PHP_VERSION}/fpm/pool.d/www.conf"
    if [[ -f "$www_pool" ]]; then
        # Remove existing settings if present
        sed -i '/^php_admin_value\[upload_max_filesize\]/d' "$www_pool"
        sed -i '/^php_admin_value\[post_max_size\]/d' "$www_pool"
        sed -i '/^php_admin_value\[max_execution_time\]/d' "$www_pool"
        sed -i '/^php_admin_value\[max_input_time\]/d' "$www_pool"
        # Add upload settings
        echo 'php_admin_value[upload_max_filesize] = 512M' >> "$www_pool"
        echo 'php_admin_value[post_max_size] = 512M' >> "$www_pool"
        echo 'php_admin_value[max_execution_time] = 600' >> "$www_pool"
        echo 'php_admin_value[max_input_time] = 600' >> "$www_pool"
    fi

    # Reload PHP-FPM
    if systemctl reload php${PHP_VERSION}-fpm 2>/dev/null; then
        log "PHP ${PHP_VERSION} configured"
    elif systemctl reload php-fpm 2>/dev/null; then
        log "PHP configured"
    else
        warn "Could not reload PHP-FPM, you may need to reload it manually"
    fi
}

# Configure MariaDB
configure_mariadb() {
    header "Configuring MariaDB"

    systemctl enable mariadb > /dev/null 2>&1
    systemctl start mariadb

    # Secure installation (non-interactive)
    mysql -e "DELETE FROM mysql.user WHERE User='';"
    mysql -e "DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost', '127.0.0.1', '::1');"
    mysql -e "DROP DATABASE IF EXISTS test;"
    mysql -e "DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';"
    mysql -e "FLUSH PRIVILEGES;"

    # Create Jabali database — reuse existing password on reinstall
    local db_password=""
    if [[ -f /root/.jabali_db_credentials ]]; then
        db_password=$(grep '^DB_PASSWORD=' /root/.jabali_db_credentials | cut -d= -f2-)
    fi
    if [[ -z "$db_password" ]] && [[ -f "$JABALI_DIR/.env" ]]; then
        db_password=$(grep '^DB_PASSWORD=' "$JABALI_DIR/.env" | cut -d= -f2-)
    fi
    if [[ -z "$db_password" ]]; then
        db_password=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)
    fi

    mysql -e "CREATE DATABASE IF NOT EXISTS jabali CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
    mysql -e "DROP USER IF EXISTS 'jabali'@'localhost';" 2>/dev/null || true
    mysql -e "DROP USER IF EXISTS 'jabali'@'127.0.0.1';" 2>/dev/null || true
    mysql -e "CREATE USER 'jabali'@'localhost' IDENTIFIED BY '${db_password}';"
    mysql -e "CREATE USER 'jabali'@'127.0.0.1' IDENTIFIED BY '${db_password}';"
    mysql -e "GRANT ALL PRIVILEGES ON jabali.* TO 'jabali'@'localhost';"
    mysql -e "GRANT ALL PRIVILEGES ON jabali.* TO 'jabali'@'127.0.0.1';"
    mysql -e "FLUSH PRIVILEGES;"

    # Save credentials
    echo "DB_PASSWORD=${db_password}" > /root/.jabali_db_credentials
    chmod 600 /root/.jabali_db_credentials

    log "MariaDB configured"
    info "Database credentials saved to /root/.jabali_db_credentials"
}

# Configure Nginx
configure_nginx() {
    header "Configuring Nginx"

    # Detect SERVER_HOSTNAME from .env or system hostname if not set (upgrade path)
    if [[ -z "${SERVER_HOSTNAME:-}" ]]; then
        if [[ -f "/var/www/jabali/.env" ]]; then
            SERVER_HOSTNAME=$(grep -oP '^PANEL_HOSTNAME=\K.*' /var/www/jabali/.env 2>/dev/null | tr -d '[:space:]')
        fi
        if [[ -z "${SERVER_HOSTNAME:-}" ]]; then
            SERVER_HOSTNAME="$(hostname -f 2>/dev/null || hostname)"
        fi
        log "Detected hostname: ${SERVER_HOSTNAME}"
    fi

    # Detect nginx version for http2 directive compatibility
    # nginx >= 1.25: use "http2 on;" directive
    # nginx < 1.25: use "listen ... http2" on the listen line
    local nginx_ver
    nginx_ver=$(nginx -v 2>&1 | grep -oP '[\d.]+' | head -1)
    if [[ "$(echo -e "1.25\n$nginx_ver" | sort -V | head -1)" == "1.25" ]]; then
        NGINX_HTTP2_LISTEN=""
        NGINX_HTTP2_DIRECTIVE="http2 on;
    "
    else
        NGINX_HTTP2_LISTEN=" http2"
        NGINX_HTTP2_DIRECTIVE=""
    fi

    # Remove default site
    rm -f /etc/nginx/sites-enabled/default

    # Configure nginx global settings for large uploads
    local nginx_conf="/etc/nginx/nginx.conf"
    if [[ -f "$nginx_conf" ]]; then
        # Add timeouts if not present
        if ! grep -q "client_body_timeout" "$nginx_conf"; then
            sed -i '/server_tokens/a\    client_max_body_size 512M;\n    client_body_timeout 600s;\n    fastcgi_read_timeout 600s;\n    proxy_read_timeout 600s;' "$nginx_conf"
        fi

        # Increase server_names_hash for hosting servers with many domains
        if ! grep -q "^[[:space:]]*server_names_hash_max_size" "$nginx_conf"; then
            sed -i '/server_tokens/a\    server_names_hash_max_size 4096;\n    server_names_hash_bucket_size 128;' "$nginx_conf"
        fi

        # Add FastCGI cache settings if not present
        if ! grep -q "fastcgi_cache_key" "$nginx_conf"; then
            # Create directory for per-user cache zone configs
            mkdir -p /etc/nginx/jabali/cache-zones

            # Add FastCGI cache configuration before the closing brace of http block
            # Per-user fastcgi_cache_path directives are managed by the Jabali agent
            sed -i '/^http {/,/^}/ {
                /include \/etc\/nginx\/sites-enabled\/\*;/a\
\
\t##\
\t# FastCGI Cache\
\t##\
\
\t# Per-user fastcgi cache paths are managed by the Jabali agent\
\t# Each user gets: fastcgi_cache_path /home/{user}/cache/nginx levels=1:2 keys_zone=JABALI_{user}:10m inactive=60m max_size=512m;\
\tfastcgi_cache_key "$scheme$request_method$host$request_uri";\
\tfastcgi_cache_use_stale error timeout invalid_header http_500 http_503;\
\tinclude /etc/nginx/jabali/cache-zones/*.conf;
            }' "$nginx_conf"
        fi

        # Enable gzip compression for all text-based content
        if ! grep -q "gzip_types" "$nginx_conf"; then
            # Uncomment existing gzip settings (with tab-indented comments)
            sed -i 's/^[[:space:]]*# gzip_vary on;/\tgzip_vary on;/' "$nginx_conf"
            sed -i 's/^[[:space:]]*# gzip_proxied any;/\tgzip_proxied any;/' "$nginx_conf"
            sed -i 's/^[[:space:]]*# gzip_comp_level 6;/\tgzip_comp_level 6;/' "$nginx_conf"
            sed -i 's/^[[:space:]]*# gzip_buffers 16 8k;/\tgzip_buffers 16 8k;/' "$nginx_conf"
            sed -i 's/^[[:space:]]*# gzip_http_version 1.1;/\tgzip_http_version 1.1;/' "$nginx_conf"
            sed -i 's/^[[:space:]]*# gzip_types .*/\tgzip_types text\/plain text\/css text\/xml text\/javascript application\/json application\/javascript application\/xml application\/xml+rss application\/x-javascript application\/vnd.ms-fontobject application\/x-font-ttf font\/opentype font\/woff font\/woff2 image\/svg+xml image\/x-icon;/' "$nginx_conf"
            # Add gzip_min_length after gzip_types to avoid compressing tiny files
            sed -i '/^[[:space:]]*gzip_types/a\	gzip_min_length 256;' "$nginx_conf"
        fi
    fi

    # Write Cloudflare real IP restoration config (idempotent)
    cat > /etc/nginx/conf.d/jabali-realip.conf << 'REALIP'
# Cloudflare real IP restoration
# Automatically trusts Cloudflare proxy IPs and extracts real client IP

# Cloudflare IPv4 ranges
set_real_ip_from 173.245.48.0/20;
set_real_ip_from 103.21.244.0/22;
set_real_ip_from 103.22.200.0/22;
set_real_ip_from 103.31.4.0/22;
set_real_ip_from 141.101.64.0/18;
set_real_ip_from 108.162.192.0/18;
set_real_ip_from 190.93.240.0/20;
set_real_ip_from 188.114.96.0/20;
set_real_ip_from 197.234.240.0/22;
set_real_ip_from 198.41.128.0/17;
set_real_ip_from 162.158.0.0/15;
set_real_ip_from 104.16.0.0/13;
set_real_ip_from 104.24.0.0/14;
set_real_ip_from 172.64.0.0/13;
set_real_ip_from 131.0.72.0/22;

# Cloudflare IPv6 ranges
set_real_ip_from 2400:cb00::/32;
set_real_ip_from 2606:4700::/32;
set_real_ip_from 2803:f800::/32;
set_real_ip_from 2405:b500::/32;
set_real_ip_from 2405:8100::/32;
set_real_ip_from 2a06:98c0::/29;
set_real_ip_from 2c0f:f248::/32;

# Local proxy
set_real_ip_from 127.0.0.1;
set_real_ip_from ::1;

# Use CF-Connecting-IP header (more reliable than X-Forwarded-For)
real_ip_header CF-Connecting-IP;
real_ip_recursive on;
REALIP
    log "Cloudflare real IP config written to /etc/nginx/conf.d/jabali-realip.conf"

    # Find PHP-FPM socket
    local php_sock=""
    local possible_sockets=(
        "/var/run/php/php${PHP_VERSION}-fpm.sock"
        "/var/run/php/php-fpm.sock"
        "/run/php/php${PHP_VERSION}-fpm.sock"
        "/run/php/php-fpm.sock"
    )

    for sock in "${possible_sockets[@]}"; do
        if [[ -S "$sock" ]] || [[ -e "$sock" ]]; then
            php_sock="$sock"
            break
        fi
    done

    # If not found, try to find it
    if [[ -z "$php_sock" ]]; then
        php_sock=$(find /var/run/php /run/php -name "*.sock" 2>/dev/null | head -1)
    fi

    # Default fallback
    if [[ -z "$php_sock" ]]; then
        php_sock="/var/run/php/php${PHP_VERSION}-fpm.sock"
        warn "PHP socket not found, using default: $php_sock"
    else
        info "Using PHP socket: $php_sock"
    fi

    # SSL certificate for nginx and FrankenPHP panel
    local ssl_dir="/etc/ssl/jabali"
    mkdir -p "$ssl_dir"

    if [[ -f "/etc/letsencrypt/live/${SERVER_HOSTNAME}/fullchain.pem" ]]; then
        # Reuse existing Let's Encrypt certificate
        cp "/etc/letsencrypt/live/${SERVER_HOSTNAME}/fullchain.pem" "$ssl_dir/panel.crt"
        cp "/etc/letsencrypt/live/${SERVER_HOSTNAME}/privkey.pem" "$ssl_dir/panel.key"
        log "Using existing Let's Encrypt certificate for ${SERVER_HOSTNAME}"
    elif [[ ! -f "$ssl_dir/panel.crt" ]] || [[ ! -f "$ssl_dir/panel.key" ]]; then
        # Generate self-signed certificate (valid for 10 years)
        log "Generating self-signed SSL certificate..."
        openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
            -keyout "$ssl_dir/panel.key" \
            -out "$ssl_dir/panel.crt" \
            -subj "/C=US/ST=State/L=City/O=Jabali Panel/CN=${SERVER_HOSTNAME:-localhost}" \
            2>/dev/null
    fi

    chown root:www-data "$ssl_dir/panel.key"
    chmod 640 "$ssl_dir/panel.key"
    chmod 644 "$ssl_dir/panel.crt"

    # Ensure Jabali Nginx include files exist for WAF/Geo includes
    local jabali_includes="/etc/nginx/jabali/includes"
    mkdir -p "$jabali_includes"
    if [[ ! -f "$jabali_includes/waf.conf" ]]; then
        echo "# Managed by Jabali — jabali-security will configure ModSecurity here" > "$jabali_includes/waf.conf"
    fi
    if [[ ! -f "$jabali_includes/geo.conf" ]]; then
        echo "# Managed by Jabali" > "$jabali_includes/geo.conf"
    fi

    # Build phpMyAdmin location block
    local phpmyadmin_block
    phpmyadmin_block=$(cat <<PHPMYADMIN_EOF
    # phpMyAdmin
    location = /phpmyadmin {
        return 301 /phpmyadmin/;
    }

    location ^~ /phpmyadmin/ {
        alias /usr/share/phpmyadmin/;
        index index.php;

        location ~ \.php\$ {
            fastcgi_pass unix:${php_sock};
            fastcgi_param SCRIPT_FILENAME \$request_filename;
            include fastcgi_params;
            fastcgi_read_timeout 600;
        }
    }
PHPMYADMIN_EOF
)

    # Build webmail location block (Bulwark webmail via Stalwart)
    local webmail_block
    webmail_block=$(cat <<WEBMAIL_EOF
    # DAV proxy (CalDAV, CardDAV, WebDAV) via Stalwart
    location = /.well-known/caldav {
        return 301 /dav/cal;
    }

    location = /.well-known/carddav {
        return 301 /dav/card;
    }

    location ^~ /dav/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    # JMAP proxy for Bulwark webmail
    location = /.well-known/jmap {
        return 301 /jmap/session;
    }

    location ^~ /jmap/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        sub_filter_types application/json;
        sub_filter_once off;
        sub_filter "http://${SERVER_HOSTNAME}:8090" "https://${SERVER_HOSTNAME}";
        sub_filter "ws://${SERVER_HOSTNAME}:8090" "wss://${SERVER_HOSTNAME}";
        sub_filter "http://127.0.0.1:8090" "https://${SERVER_HOSTNAME}";
    }

    # Bulwark webmail branding assets
    location /branding/ {
        alias /opt/bulwark/public/branding/;
        expires 7d;
        access_log off;
    }

    # Bulwark webmail static assets (served directly for correct MIME types)
    location ^~ /webmail/_next/static/ {
        alias /opt/bulwark/.next/static/;
        expires 365d;
        add_header Cache-Control "public, max-age=31536000, immutable";
        access_log off;
    }

    # Bulwark webmail
    location = /webmail {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location ^~ /webmail/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
    }
WEBMAIL_EOF
)

    # Remove old panel vhosts that aren't the current hostname (prevents default_server conflicts)
    for old_vhost in /etc/nginx/sites-enabled/*; do
        [[ -f "$old_vhost" ]] || continue
        local vhost_name
        vhost_name="$(basename "$old_vhost")"
        # Skip user domain configs (they have .conf extension) and the current hostname
        if [[ "$vhost_name" == "${SERVER_HOSTNAME}" ]] || [[ "$vhost_name" == "default" ]]; then
            continue
        fi
        # Only remove if it has default_server (panel vhost marker)
        if [[ ! "$vhost_name" == *.conf ]] && grep -q 'default_server' "$old_vhost" 2>/dev/null; then
            rm -f "$old_vhost"
            rm -f "/etc/nginx/sites-available/$vhost_name"
            log "Removed stale panel vhost: $vhost_name"
        fi
    done

    # Create Jabali site config with HTTP redirect and HTTPS for phpMyAdmin/webmail
    cat > /etc/nginx/sites-available/${SERVER_HOSTNAME} << NGINX
# HTTP — redirect to HTTPS, ACME challenge proxy, health check
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name ${SERVER_HOSTNAME} _;

    # Mail autoconfig/autodiscover over HTTP (avoids SSL cert mismatch for subdomains)
    location = /mail/config-v1.1.xml {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location = /.well-known/autoconfig/mail/config-v1.1.xml {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /autodiscover/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /Autodiscover/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    # ACME challenge proxy — FrankenPHP/Caddy handles certificate issuance
    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }

    # Health check — proxy to FrankenPHP panel
    location = /up {
        proxy_pass https://127.0.0.1:${PANEL_PORT};
        proxy_ssl_verify off;
    }

    location / {
        return 301 https://\$host\$request_uri;
    }
}

# HTTPS — phpMyAdmin and webmail only (panel served by FrankenPHP on port ${PANEL_PORT})
server {
    listen 443 ssl${NGINX_HTTP2_LISTEN} default_server;
    listen [::]:443 ssl${NGINX_HTTP2_LISTEN} default_server;

    server_name _;

    ssl_certificate /etc/ssl/jabali/panel.crt;
    ssl_certificate_key /etc/ssl/jabali/panel.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    # Redirect panel paths to FrankenPHP
    location ~ ^/(jabali-admin|jabali-panel|livewire|login|up) {
        return 301 https://\$host:${PANEL_PORT}\$request_uri;
    }
    location = / {
        return 301 https://\$host:${PANEL_PORT}/;
    }

${phpmyadmin_block}

${webmail_block}

    # Deny everything else
    location / {
        return 404;
    }
}
NGINX

    ln -sf /etc/nginx/sites-available/${SERVER_HOSTNAME} /etc/nginx/sites-enabled/

    # Create nginx pre-start script to ensure log directories exist
    # This prevents nginx from failing if a user deletes their logs directory
    cat > /usr/local/bin/nginx-ensure-logs << 'ENSURELOG'
#!/bin/bash
for conf in /etc/nginx/sites-enabled/*; do
    if [ -f "$conf" ]; then
        grep -oP '(access_log|error_log)\s+\K[^\s;]+' "$conf" 2>/dev/null | while read -r logpath; do
            if [[ "$logpath" != "off" && "$logpath" != "/dev/null" && "$logpath" != syslog* && "$logpath" == /* ]]; then
                logdir=$(dirname "$logpath")
                if [ ! -d "$logdir" ]; then
                    mkdir -p "$logdir"
                    if [[ "$logdir" =~ ^/home/([^/]+)/ ]]; then
                        username="${BASH_REMATCH[1]}"
                        id "$username" &>/dev/null && chown "$username:$username" "$logdir"
                    fi
                fi
            fi
        done
    fi
done
exit 0
ENSURELOG
    chmod +x /usr/local/bin/nginx-ensure-logs

    # Add systemd override to run the script before nginx starts
    mkdir -p /etc/systemd/system/nginx.service.d
    cat > /etc/systemd/system/nginx.service.d/ensure-logs.conf << 'OVERRIDE'
[Service]
ExecStartPre=
ExecStartPre=/usr/local/bin/nginx-ensure-logs
ExecStartPre=/usr/sbin/nginx -t -q -g 'daemon on; master_process on;'
OVERRIDE
    systemctl daemon-reload

    # Start or reload nginx depending on whether it's already running
    if nginx -t -q 2>/dev/null; then
        systemctl enable nginx > /dev/null 2>&1
        if systemctl is-active --quiet nginx; then
            systemctl reload nginx
        else
            systemctl start nginx
        fi
    fi

    log "Nginx configured with HTTPS (self-signed certificate)"
}

# Configure Stalwart Mail Server (SMTP, IMAP, JMAP, DKIM, spam filter — single binary)
configure_stalwart() {
    header "Configuring Stalwart Mail Server"

    # Create vmail user (same UID/GID as legacy for migration compat)
    mkdir -p /var/mail/vhosts
    groupadd -g 5000 vmail 2>/dev/null || true
    useradd -g vmail -u 5000 vmail -d /var/mail 2>/dev/null || true
    chown -R vmail:vmail /var/mail

    # Install Stalwart Mail Server
    info "Installing Stalwart Mail Server..."
    if ! command -v stalwart >/dev/null 2>&1 && [[ ! -x /usr/local/bin/stalwart ]]; then
        # Map dpkg architecture to Rust target triple
        local dpkg_arch
        dpkg_arch=$(dpkg --print-architecture)
        local rust_arch
        case "$dpkg_arch" in
            amd64)  rust_arch="x86_64" ;;
            arm64)  rust_arch="aarch64" ;;
            armhf)  rust_arch="armv7" ;;
            *)      error "Unsupported architecture: $dpkg_arch"; return 1 ;;
        esac

        local stalwart_url="https://github.com/stalwartlabs/mail-server/releases/latest/download/stalwart-${rust_arch}-unknown-linux-gnu.tar.gz"

        info "Downloading Stalwart Mail Server..."
        local tmp_dir
        tmp_dir=$(mktemp -d)
        if curl -fsSL "$stalwart_url" -o "${tmp_dir}/stalwart.tar.gz"; then
            tar -xzf "${tmp_dir}/stalwart.tar.gz" -C "${tmp_dir}"
            install -m 755 "${tmp_dir}/stalwart" /usr/local/bin/stalwart
            rm -rf "${tmp_dir}"
            log "Stalwart Mail Server installed to /usr/local/bin/stalwart"
        else
            rm -rf "${tmp_dir}"
            error "Failed to download Stalwart Mail Server"
            return 1
        fi
    else
        info "Stalwart already installed"
    fi

    # Create Stalwart directories
    mkdir -p /etc/stalwart-mail
    mkdir -p /etc/stalwart-mail/acme
    mkdir -p /var/lib/stalwart-mail
    mkdir -p /var/log/stalwart-mail

    # Read DB credentials
    local _db_pass=""
    local _db_host="${DB_HOST:-127.0.0.1}"
    local _db_user="${DB_USERNAME:-jabali}"
    local _db_name="${DB_DATABASE:-jabali}"
    if [[ -f /root/.jabali_db_credentials ]]; then
        _db_pass=$(grep '^DB_PASSWORD=' /root/.jabali_db_credentials | cut -d= -f2-)
    elif [[ -f "$JABALI_DIR/.env" ]]; then
        _db_pass=$(grep '^DB_PASSWORD=' "$JABALI_DIR/.env" | cut -d= -f2-)
    fi

    # Generate admin API token
    local api_token
    api_token=$(openssl rand -hex 32)

    mkdir -p /etc/jabali
    cat > /etc/jabali/stalwart-api.conf <<EOF
STALWART_API_TOKEN=${api_token}
EOF
    chown root:www-data /etc/jabali/stalwart-api.conf
    chmod 640 /etc/jabali/stalwart-api.conf

    # Create SSO token directory for webmail auto-login
    mkdir -p /var/lib/jabali/sso-tokens
    chown www-data:www-data /var/lib/jabali/sso-tokens
    chmod 700 /var/lib/jabali/sso-tokens

    # URL-encode the DB password for the connection string
    local _db_pass_encoded
    _db_pass_encoded=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${_db_pass}', safe=''))" 2>/dev/null || echo "${_db_pass}")

    # Write Stalwart TOML configuration
    # Stalwart 0.15.x requires plaintext secret for fallback-admin
    local admin_hash="${api_token}"

    # Use stalwart --init to generate base config, then customize
    local stalwart_data="/var/lib/stalwart-mail"

    cat > /etc/stalwart-mail/config.toml <<STALWART_CONF
# Stalwart Mail Server Configuration - Managed by Jabali Panel

[server.listener.smtp]
bind = ["[::]:25"]
protocol = "smtp"

[server.listener.submission]
bind = ["[::]:587"]
protocol = "smtp"

[server.listener.submissions]
bind = ["[::]:465"]
protocol = "smtp"
tls.implicit = true

[server.listener.imap]
bind = ["[::]:143"]
protocol = "imap"

[server.listener.imaptls]
bind = ["[::]:993"]
protocol = "imap"
tls.implicit = true

[server.listener.pop3s]
bind = ["[::]:995"]
protocol = "pop3"
tls.implicit = true

[server.listener.sieve]
bind = ["[::]:4190"]
protocol = "managesieve"

[server.listener.http]
protocol = "http"
bind = ["[::]:8090"]

[email.folders.sent]
name = "Sent"
create = true
subscribe = true

[email.folders.trash]
name = "Trash"
create = true
subscribe = true

[email.folders.junk]
name = "Junk"
create = true
subscribe = true

[email.folders.drafts]
name = "Drafts"
create = true
subscribe = true

[email.folders.archive]
name = "Archive"
create = true
subscribe = true

[calendar.default]
display-name = "Personal"

[contacts.default]
display-name = "Personal"

[storage]
data = "rocksdb"
fts = "rocksdb"
blob = "rocksdb"
lookup = "rocksdb"
directory = "internal"

[store.rocksdb]
type = "rocksdb"
path = "${stalwart_data}/data"
compression = "lz4"

[directory.internal]
type = "internal"
store = "rocksdb"

[tracer.log]
type = "log"
level = "info"
path = "/var/log/stalwart-mail"
prefix = "stalwart.log"
rotate = "daily"
ansi = false
enable = true

# TLS: certbot issues certs, tls.toml overrides this default on LE-enabled installs
[certificate.default]
cert = "/etc/ssl/jabali/panel.crt"
private-key = "/etc/ssl/jabali/panel.key"

[auth.dkim]
sign = [ { if = "listener != 'smtp'", then = "[]" },
         { else = false } ]

[authentication.fallback-admin]
user = "admin"
secret = "${admin_hash}"
STALWART_CONF

    chmod 640 /etc/stalwart-mail/config.toml

    # Create systemd service if not in container
    if [[ ! -f /.dockerenv ]] && [[ ! -f /run/.containerenv ]]; then
        cat > /etc/systemd/system/stalwart-mail.service <<'SYSTEMD'
[Unit]
Description=Stalwart Mail Server
After=network.target mysql.service mariadb.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/stalwart --config /etc/stalwart-mail/config.toml
Restart=always
RestartSec=5
# TODO: Run as dedicated 'stalwart' user instead of root (requires user creation and port capabilities)
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
SYSTEMD
        systemctl daemon-reload
        systemctl enable stalwart-mail > /dev/null 2>&1
        systemctl start stalwart-mail
    fi

    log "Stalwart Mail Server configured"

    # Install Bulwark Webmail (JMAP client for Stalwart)
    # This runs in a subshell so failures never abort the main install
    info "Installing Bulwark Webmail..."
    (
        set +e
        local bulwark_dir="/opt/bulwark"

        # Ensure Node.js is available
        if ! command -v node >/dev/null 2>&1; then
            info "Installing Node.js for Bulwark..."
            curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1
            DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs >/dev/null 2>&1
        fi

        if ! command -v node >/dev/null 2>&1; then
            warn "Node.js not available — skipping Bulwark webmail"
            exit 0
        fi

        # Clone Bulwark if not present
        if [[ ! -d "${bulwark_dir}/.git" ]]; then
            rm -rf "$bulwark_dir"
            if ! git clone --depth 1 https://github.com/bulwarkmail/webmail.git "$bulwark_dir" 2>/dev/null; then
                warn "Could not clone Bulwark repository — webmail will not be available"
                exit 0
            fi
        else
            # Update existing clone
            cd "$bulwark_dir"
            git fetch --depth 1 origin main 2>/dev/null && git reset --hard origin/main 2>/dev/null || true
        fi

        cd "$bulwark_dir"

        # Preserve SESSION_SECRET across rebuilds
        local existing_secret=""
        if [[ -f .env.local ]]; then
            existing_secret=$(grep '^SESSION_SECRET=' .env.local 2>/dev/null | cut -d= -f2)
        fi

        cat > .env.local <<BULWARK_ENV
JMAP_SERVER_URL=http://127.0.0.1:8090
HOSTNAME=127.0.0.1
PORT=3000
APP_NAME=Webmail
SESSION_SECRET=${existing_secret:-$(openssl rand -base64 32)}
BULWARK_ENV

            # Apply all Jabali patches (basePath, SSO, auth-store, proxy, fetch paths)
            patch_bulwark "$bulwark_dir"

            info "Building Bulwark (this may take a minute)..."
            npm install >/dev/null 2>&1
            npm run build >/dev/null 2>&1

            if [[ ! -f "${bulwark_dir}/.next/BUILD_ID" ]]; then
                warn "Bulwark build failed — webmail will not be available"
                exit 0
            fi

            # Standalone output requires static, public, and env to be symlinked
            ln -sfn "${bulwark_dir}/.next/static" "${bulwark_dir}/.next/standalone/.next/static"
            ln -sfn "${bulwark_dir}/public" "${bulwark_dir}/.next/standalone/public"
            ln -sfn "${bulwark_dir}/.env.local" "${bulwark_dir}/.next/standalone/.env.local"

            chown -R www-data:www-data "${bulwark_dir}/.next" "${bulwark_dir}/.env.local"
            log "Bulwark Webmail built successfully"

        # Create systemd service (bare metal only)
        if [[ ! -f /.dockerenv ]] && [[ ! -f /run/.containerenv ]]; then
            cat > /etc/systemd/system/bulwark.service <<BULWARK_SVC
[Unit]
Description=Bulwark Webmail
After=network.target stalwart-mail.service

[Service]
Type=simple
WorkingDirectory=${bulwark_dir}
ExecStart=/usr/bin/node ${bulwark_dir}/.next/standalone/server.js
Restart=always
RestartSec=5
User=www-data
EnvironmentFile=${bulwark_dir}/.env.local
Environment=NODE_ENV=production HOSTNAME=127.0.0.1 PORT=3000 NODE_TLS_REJECT_UNAUTHORIZED=0

[Install]
WantedBy=multi-user.target
BULWARK_SVC
            systemctl daemon-reload
            systemctl enable bulwark > /dev/null 2>&1
            systemctl start bulwark
            log "Bulwark Webmail service started on port 3000"
        fi
    ) || warn "Bulwark installation encountered errors"

    cd "$JABALI_DIR"
    log "Stalwart Mail Server configured with Bulwark Webmail"
}

# Create webmaster mailbox for system notifications
create_webmaster_mailbox() {
    if [[ "$INSTALL_MAIL" != "true" ]]; then
        info "Skipping webmaster mailbox creation (mail server not installed)"
        return
    fi

    info "Creating webmaster mailbox..."

    # Ensure Stalwart is running and its HTTP API is reachable
    systemctl start stalwart-mail 2>/dev/null || true
    local _wait=0
    while ! curl -sf "http://127.0.0.1:8090/.well-known/jmap" >/dev/null 2>&1; do
        sleep 1
        _wait=$((_wait + 1))
        if [[ $_wait -ge 15 ]]; then
            warn "Stalwart HTTP API not reachable after 15s — webmaster mailbox may lack Stalwart account"
            break
        fi
    done

    local webmaster_password=$(openssl rand -base64 24 | tr -dc 'a-zA-Z0-9!@#$%' | head -c 16)

    # Extract root domain for email (e.g., panel.example.com -> example.com)
    local dot_count=$(echo "$SERVER_HOSTNAME" | tr -cd '.' | wc -c)
    local email_domain="$SERVER_HOSTNAME"
    if [[ $dot_count -gt 1 ]]; then
        email_domain=$(echo "$SERVER_HOSTNAME" | awk -F. '{print $(NF-1)"."$NF}')
    fi

    cd "$JABALI_DIR"
    php artisan tinker --execute="
        use App\Models\DnsRecord;
        use App\Models\DnsSetting;
        use App\Models\Domain;
        use App\Models\EmailDomain;
        use App\Models\Mailbox;
        use App\Models\User;
        use App\Services\Agent\AgentClient;
        use Illuminate\Support\Facades\Crypt;

        \$hostname = '${email_domain}';
        \$password = '${webmaster_password}';

        // Find admin user
        \$admin = User::where('is_admin', true)->first();
        if (!\$admin) {
            echo 'No admin user found, skipping webmaster mailbox';
            return;
        }

        // Ensure system user exists for admin
        \$agent = new AgentClient();
        if (!posix_getpwnam(\$admin->username)) {
            \$agent->send('user.create', [
                'username' => \$admin->username,
                'password' => '',
            ]);
        }

        // Create or find domain for hostname
        \$domain = Domain::where('domain', \$hostname)->first();

        if (!\$domain) {
            // Create via agent first (sets up vhost + document root directory)
            try {
                \$agent->domainCreate(\$admin->username, \$hostname);
            } catch (Exception \$e) {
                // May fail if vhost already exists
            }

            \$domain = Domain::create([
                'user_id' => \$admin->id,
                'domain' => \$hostname,
                'document_root' => '/home/' . \$admin->username . '/domains/' . \$hostname . '/public_html',
                'is_active' => true,
                'ssl_enabled' => false,
                'directory_index' => 'index.php index.html',
            ]);
        }

        // Enable email for domain if not already
        try {
            \$agent->emailEnableDomain(\$admin->username, \$hostname);
        } catch (Exception \$e) {
            // Domain might already be enabled
        }

        // Create EmailDomain record
        \$emailDomain = EmailDomain::firstOrCreate(
            ['domain_id' => \$domain->id],
            ['is_active' => true]
        );

        // Generate DKIM keys for the domain
        try {
            \$dkimResult = \$agent->emailGenerateDkim(\$admin->username, \$hostname);
            if (isset(\$dkimResult['public_key'])) {
                \$selector = \$dkimResult['selector'] ?? 'default';
                \$publicKey = \$dkimResult['public_key'];

                \$emailDomain->update([
                    'dkim_selector' => \$selector,
                    'dkim_public_key' => \$publicKey,
                    'dkim_private_key' => \$dkimResult['private_key'] ?? null,
                ]);

                // Add DKIM DNS record
                \$cleanKey = preg_replace('/-----[A-Z ]+-----|\\s/', '', \$publicKey);
                \$dkimContent = 'v=DKIM1; k=rsa; p=' . \$cleanKey;

                DnsRecord::firstOrCreate(
                    [
                        'domain_id' => \$domain->id,
                        'name' => \$selector . '._domainkey',
                        'type' => 'TXT',
                    ],
                    [
                        'content' => \$dkimContent,
                        'ttl' => 3600,
                    ]
                );

                // Sync DNS zone with full records from DB
                try {
                    \$settings = DnsSetting::getAll();
                    \$allRecords = DnsRecord::where('domain_id', \$domain->id)->get()->toArray();
                    \$serverIp = trim(shell_exec('hostname -I') ?: '');
                    \$serverIp = explode(' ', \$serverIp)[0] ?? '127.0.0.1';
                    \$agent->send('dns.sync_zone', [
                        'domain' => \$hostname,
                        'records' => \$allRecords,
                        'ns1' => \$settings['ns1'] ?? 'ns1.' . \$hostname,
                        'ns2' => \$settings['ns2'] ?? 'ns2.' . \$hostname,
                        'admin_email' => \$settings['admin_email'] ?? 'admin.' . \$hostname,
                        'default_ip' => \$settings['default_ip'] ?? \$serverIp,
                        'default_ttl' => \$settings['default_ttl'] ?? 3600,
                    ]);
                } catch (Exception \$e) {
                    // DNS sync is best-effort
                }
            }
        } catch (Exception \$e) {
            // DKIM generation is best-effort during install
        }

        // Check if webmaster mailbox exists
        if (Mailbox::where('email_domain_id', \$emailDomain->id)->where('local_part', 'webmaster')->exists()) {
            echo 'Webmaster mailbox already exists';
            return;
        }

        // Create mailbox via agent
        \$result = \$agent->mailboxCreate(\$admin->username, 'webmaster@' . \$hostname, \$password, 1073741824);

        Mailbox::create([
            'email_domain_id' => \$emailDomain->id,
            'user_id' => \$admin->id,
            'local_part' => 'webmaster',
            'password_hash' => \$result['password_hash'] ?? '',
            'password_encrypted' => Crypt::encryptString(\$password),
            'maildir_path' => \$result['maildir_path'] ?? null,
            'system_uid' => \$result['uid'] ?? null,
            'system_gid' => \$result['gid'] ?? null,
            'name' => 'System Webmaster',
            'quota_bytes' => 1073741824,
            'is_active' => true,
        ]);

        echo 'Webmaster mailbox created successfully';
    " 2>/dev/null || true

    # Export for print_completion()
    WEBMASTER_EMAIL="webmaster@${SERVER_HOSTNAME}"
    WEBMASTER_PASSWORD="${webmaster_password}"

    log "Webmaster mailbox: ${WEBMASTER_EMAIL}"

    # Configure authenticated SMTP in .env using the webmaster mailbox
    local mail_host="mail.${SERVER_HOSTNAME}"
    # If hostname already starts with mail., use it directly
    if [[ "$SERVER_HOSTNAME" == mail.* ]]; then
        mail_host="$SERVER_HOSTNAME"
    fi

    cd "$JABALI_DIR"
    sed -i "s/^MAIL_HOST=.*/MAIL_HOST=${mail_host}/" .env
    sed -i "s/^MAIL_PORT=.*/MAIL_PORT=587/" .env
    sed -i "s/^MAIL_ENCRYPTION=.*/MAIL_ENCRYPTION=tls/" .env
    sed -i "s/^MAIL_USERNAME=.*/MAIL_USERNAME=webmaster@${SERVER_HOSTNAME}/" .env
    sed -i "s/^MAIL_PASSWORD=.*/MAIL_PASSWORD=${webmaster_password}/" .env
    php artisan config:cache --quiet 2>/dev/null || true

    log "SMTP configured via ${mail_host}:587"

    # Save credentials
    echo "" >> /root/jabali_credentials.txt
    echo "=== Webmaster Email ===" >> /root/jabali_credentials.txt
    echo "Email: webmaster@${SERVER_HOSTNAME}" >> /root/jabali_credentials.txt
    echo "Password: ${webmaster_password}" >> /root/jabali_credentials.txt
}

# Configure SSH login notifications via PAM
configure_ssh_notifications() {
    header "Configuring SSH Login Notifications"

    # Create PAM hook script in /etc/security
    local pam_script="/etc/security/jabali-ssh-notify.sh"

    cat > "$pam_script" << 'PAMSCRIPT'
#!/bin/bash
# Jabali SSH Login Notification Hook
# Called by PAM on successful SSH authentication

# Only run on successful authentication
if [ "$PAM_TYPE" != "open_session" ]; then
    exit 0
fi

# Get the username and IP
USERNAME="$PAM_USER"
IP="${PAM_RHOST:-unknown}"

# Determine auth method
if [ -n "$SSH_AUTH_INFO_0" ]; then
    case "$SSH_AUTH_INFO_0" in
        publickey*) METHOD="publickey" ;;
        password*) METHOD="password" ;;
        keyboard-interactive*) METHOD="keyboard-interactive" ;;
        *) METHOD="password" ;;
    esac
else
    METHOD="password"
fi

# Run the notification command in background
cd /var/www/jabali && /usr/bin/php artisan notify:ssh-login "$USERNAME" "$IP" --method="$METHOD" > /dev/null 2>&1 &

exit 0
PAMSCRIPT

    chmod +x "$pam_script"

    # Add to PAM sshd configuration if not already present
    local pam_sshd="/etc/pam.d/sshd"
    if ! grep -q "jabali-ssh-notify" "$pam_sshd" 2>/dev/null; then
        echo "# Jabali SSH login notification" >> "$pam_sshd"
        echo "session optional pam_exec.so quiet /etc/security/jabali-ssh-notify.sh" >> "$pam_sshd"
        log "Added SSH login notification hook to PAM"
    else
        info "SSH login notification hook already configured"
    fi

    log "SSH login notifications configured"
}

# Configure DNS Zone for server hostname
configure_dns() {
    header "Configuring PowerDNS"

    local server_ip=$(hostname -I | awk '{print $1}')
    local db_pass=""
    local db_user="jabali"

    if [[ -f /root/.jabali_db_credentials ]]; then
        db_pass=$(grep '^DB_PASSWORD=' /root/.jabali_db_credentials | cut -d= -f2-)
    fi

    # Create PowerDNS database
    info "Creating PowerDNS database..."
    mysql -u root <<SQL
CREATE DATABASE IF NOT EXISTS powerdns;
GRANT ALL ON powerdns.* TO '${db_user}'@'localhost';
FLUSH PRIVILEGES;
SQL

    # Import schema — check if tables exist first
    local has_tables=$(mysql -u root -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='powerdns'" 2>/dev/null)
    if [[ "$has_tables" -eq 0 ]] || [[ -z "$has_tables" ]]; then
        # Try package-provided schema file first (exclude migration files like 3.4.0_to_4.1.0_*)
        local schema_file=$(find /usr/share -name "schema.mysql.sql" -path "*pdns*" 2>/dev/null | head -1)
        if [[ -z "$schema_file" ]]; then
            # Try alternate naming: 4.*.schema.mysql.sql but NOT X_to_X migration files
            schema_file=$(find /usr/share -name "*.mysql.sql" -path "*pdns*" ! -name "*_to_*" 2>/dev/null | head -1)
        fi
        if [[ -n "$schema_file" ]]; then
            mysql -u root powerdns < "$schema_file" 2>/dev/null || true
            log "PowerDNS schema imported from ${schema_file}"
        else
            # Fallback: create tables inline (PowerDNS 4.x MySQL backend)
            mysql -u root powerdns <<'SCHEMA'
CREATE TABLE IF NOT EXISTS domains (
  id INT AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(255) NOT NULL UNIQUE,
  master VARCHAR(128) DEFAULT NULL,
  last_check INT DEFAULT NULL,
  type VARCHAR(8) NOT NULL DEFAULT 'NATIVE',
  notified_serial INT UNSIGNED DEFAULT NULL,
  account VARCHAR(40) DEFAULT NULL
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS records (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  domain_id INT DEFAULT NULL,
  name VARCHAR(255) DEFAULT NULL,
  type VARCHAR(10) DEFAULT NULL,
  content VARCHAR(65535) DEFAULT NULL,
  ttl INT DEFAULT NULL,
  prio INT DEFAULT NULL,
  disabled TINYINT(1) DEFAULT 0,
  ordername VARCHAR(255) DEFAULT NULL,
  auth TINYINT(1) DEFAULT 1,
  CONSTRAINT FOREIGN KEY (domain_id) REFERENCES domains(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE INDEX IF NOT EXISTS nametype_index ON records(name, type);
CREATE INDEX IF NOT EXISTS domain_id ON records(domain_id);
CREATE INDEX IF NOT EXISTS ordername ON records(ordername);

CREATE TABLE IF NOT EXISTS supermasters (
  ip VARCHAR(64) NOT NULL,
  nameserver VARCHAR(255) NOT NULL,
  account VARCHAR(40) NOT NULL,
  PRIMARY KEY(ip, nameserver)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS comments (
  id INT AUTO_INCREMENT PRIMARY KEY,
  domain_id INT NOT NULL,
  name VARCHAR(255) NOT NULL,
  type VARCHAR(10) NOT NULL,
  modified_at INT NOT NULL,
  account VARCHAR(40) DEFAULT NULL,
  comment TEXT NOT NULL,
  CONSTRAINT FOREIGN KEY (domain_id) REFERENCES domains(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE INDEX IF NOT EXISTS comments_name_type_idx ON comments(name, type);
CREATE INDEX IF NOT EXISTS comments_order_idx ON comments(domain_id, modified_at);

CREATE TABLE IF NOT EXISTS domainmetadata (
  id INT AUTO_INCREMENT PRIMARY KEY,
  domain_id INT NOT NULL,
  kind VARCHAR(32) NOT NULL,
  content TEXT,
  CONSTRAINT FOREIGN KEY (domain_id) REFERENCES domains(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE INDEX IF NOT EXISTS domainmetadata_idx ON domainmetadata(domain_id, kind);

CREATE TABLE IF NOT EXISTS cryptokeys (
  id INT AUTO_INCREMENT PRIMARY KEY,
  domain_id INT NOT NULL,
  flags INT NOT NULL DEFAULT 0,
  active BOOL DEFAULT 1,
  published BOOL DEFAULT 1,
  content TEXT,
  CONSTRAINT FOREIGN KEY (domain_id) REFERENCES domains(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE INDEX IF NOT EXISTS domainidindex ON cryptokeys(domain_id);

CREATE TABLE IF NOT EXISTS tsigkeys (
  id INT AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  algorithm VARCHAR(50) NOT NULL,
  secret VARCHAR(255) NOT NULL,
  CONSTRAINT UNIQUE KEY (name, algorithm)
) ENGINE=InnoDB;
SCHEMA
            log "PowerDNS schema created (inline fallback)"
        fi
    else
        info "PowerDNS tables already exist"
    fi

    # Generate API key
    local pdns_api_key=$(openssl rand -hex 32)
    mkdir -p /etc/jabali
    echo "POWERDNS_API_KEY=${pdns_api_key}" > /etc/jabali/powerdns-api.conf
    chown root:www-data /etc/jabali/powerdns-api.conf
    chmod 640 /etc/jabali/powerdns-api.conf

    # Remove default bind backend config
    rm -f /etc/powerdns/pdns.d/bind.conf 2>/dev/null
    # Comment out default launch= in main config
    sed -i "s/^launch=$/# launch=/" /etc/powerdns/pdns.conf 2>/dev/null

    # Write Jabali PowerDNS config
    cat > /etc/powerdns/pdns.d/jabali.conf <<PDNSCONF
# Jabali Panel — PowerDNS configuration
launch=gmysql
gmysql-host=127.0.0.1
gmysql-user=${db_user}
gmysql-password=${db_pass}
gmysql-dbname=powerdns

api=yes
api-key=${pdns_api_key}
webserver=yes
webserver-address=127.0.0.1
webserver-port=8081
webserver-allow-from=127.0.0.1

local-address=0.0.0.0
local-port=53

default-soa-content=ns1.@ hostmaster.@ 0 10800 3600 604800 3600
log-dns-queries=no
security-poll-suffix=
PDNSCONF

    # Remove BIND9/named if installed (conflicts with PowerDNS on port 53)
    if dpkg -l bind9 2>/dev/null | grep -q '^ii'; then
        info "Removing BIND9 (conflicts with PowerDNS)..."
        systemctl stop bind9 2>/dev/null || true
        systemctl stop named 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get purge -y bind9 bind9-utils bind9-dnsutils 2>/dev/null || true
    fi
    systemctl stop named 2>/dev/null || true
    systemctl disable named 2>/dev/null || true

    # Disable systemd-resolved if it holds port 53
    if ss -tlnp | grep -q ':53 .*systemd-resolve'; then
        info "Disabling systemd-resolved (conflicts with PowerDNS on port 53)..."
        systemctl stop systemd-resolved 2>/dev/null || true
        systemctl disable systemd-resolved 2>/dev/null || true
        # Point resolv.conf to external DNS
        rm -f /etc/resolv.conf
        echo -e "nameserver 1.1.1.1\nnameserver 8.8.8.8" > /etc/resolv.conf
    fi

    # Enable and start PowerDNS
    systemctl enable pdns 2>/dev/null || true
    systemctl restart pdns

    log "PowerDNS configured"

    # Extract domain from hostname (e.g., panel.example.com -> example.com)
    local domain=$(echo "$SERVER_HOSTNAME" | awk -F. '{if (NF>2) {print $(NF-1)"."$NF} else {print $0}}')
    local hostname_part=$(echo "$SERVER_HOSTNAME" | sed "s/\.${domain}$//")
    if [[ "$hostname_part" == "$domain" ]]; then
        hostname_part=""
    fi

    # Create initial zone for the server's domain via PowerDNS API
    sleep 2  # Give PowerDNS time to fully start

    # Skip if zone already exists (DomainObserver may have created it)
    local zone_check=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "X-API-Key: ${pdns_api_key}" \
        "http://127.0.0.1:8081/api/v1/servers/localhost/zones/${domain}." 2>/dev/null)

    if [[ "$zone_check" == "200" ]]; then
        info "DNS zone for $domain already exists"
    else
        info "Creating DNS zone for $domain..."
    fi

    # Create zone (idempotent — ignored if exists)
    curl -s -X POST \
        -H "X-API-Key: ${pdns_api_key}" \
        -H "Content-Type: application/json" \
        -d "{\"name\":\"${domain}.\",\"kind\":\"Native\",\"nameservers\":[\"ns1.${domain}.\",\"ns2.${domain}.\"]}" \
        http://127.0.0.1:8081/api/v1/servers/localhost/zones >/dev/null 2>&1

    # Build records payload
    local records="["
    # NS records
    records+="{\"name\":\"${domain}.\",\"type\":\"NS\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"ns1.${domain}.\",\"disabled\":false},{\"content\":\"ns2.${domain}.\",\"disabled\":false}]},"
    # A records: @, ns1, ns2, mail, autoconfig, autodiscover, www
    records+="{\"name\":\"${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"ns1.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"ns2.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"mail.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"autoconfig.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"autodiscover.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    records+="{\"name\":\"www.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    # Hostname A record if it's a subdomain
    if [[ -n "$hostname_part" ]]; then
        records+="{\"name\":\"${hostname_part}.${domain}.\",\"type\":\"A\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"${server_ip}\",\"disabled\":false}]},"
    fi
    # MX record
    records+="{\"name\":\"${domain}.\",\"type\":\"MX\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"10 mail.${domain}.\",\"disabled\":false}]},"
    # TXT records (SPF, DMARC)
    records+="{\"name\":\"${domain}.\",\"type\":\"TXT\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"\\\"v=spf1 mx a ip4:${server_ip} ~all\\\"\",\"disabled\":false}]},"
    records+="{\"name\":\"_dmarc.${domain}.\",\"type\":\"TXT\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"\\\"v=DMARC1; p=none; rua=mailto:admin@${domain}\\\"\",\"disabled\":false}]},"
    # SRV records for mail client auto-discovery
    records+="{\"name\":\"_imaps._tcp.${domain}.\",\"type\":\"SRV\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"0 1 993 mail.${domain}.\",\"disabled\":false}]},"
    records+="{\"name\":\"_pop3s._tcp.${domain}.\",\"type\":\"SRV\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"0 1 995 mail.${domain}.\",\"disabled\":false}]},"
    records+="{\"name\":\"_submission._tcp.${domain}.\",\"type\":\"SRV\",\"ttl\":3600,\"changetype\":\"REPLACE\",\"records\":[{\"content\":\"0 1 587 mail.${domain}.\",\"disabled\":false}]}"
    records+="]"

    # Apply records
    curl -s -X PATCH \
        -H "X-API-Key: ${pdns_api_key}" \
        -H "Content-Type: application/json" \
        -d "{\"rrsets\":${records}}" \
        "http://127.0.0.1:8081/api/v1/servers/localhost/zones/${domain}." >/dev/null 2>&1

    log "DNS zone created for $domain"

    echo ""
    echo -e "${YELLOW}Important DNS Setup:${NC}"
    echo "Point your domain's nameservers to this server:"
    echo "  ns1.$domain -> $server_ip"
    echo "  ns2.$domain -> $server_ip"
    echo ""
}

# Setup Disk Quotas
setup_quotas() {
    header "Setting Up Disk Quotas"

    # Check if quotas are already enabled on root filesystem
    if mount | grep ' / ' | grep -q 'usrquota'; then
        log "Quotas already enabled on root filesystem"
        return 0
    fi

    # Get the root filesystem device
    local root_dev=$(findmnt -n -o SOURCE /)
    local root_mount=$(findmnt -n -o TARGET /)

    if [[ -z "$root_dev" ]]; then
        warn "Could not determine root filesystem device"
        return 1
    fi

    info "Enabling quotas on $root_dev ($root_mount)..."

    # Check filesystem type
    local fs_type=$(findmnt -n -o FSTYPE /)

    # Detect ZFS filesystem — quotas managed natively by ZFS, skip Linux quota setup
    if [[ "$fs_type" == "zfs" ]]; then
        log "ZFS filesystem detected — skipping Linux quota setup"
        info "ZFS manages quotas natively. Configure via: zfs set userquota@<user>=<size> <dataset>"
        return 0
    fi

    if [[ "$fs_type" != "ext4" && "$fs_type" != "ext3" && "$fs_type" != "xfs" ]]; then
        warn "Quotas not supported on $fs_type filesystem"
        return 1
    fi

    # For ext4, try to enable quota feature directly (modern approach)
    if [[ "$fs_type" == "ext4" ]]; then
        # Check if quota feature is available
        if tune2fs -l "$root_dev" 2>/dev/null | grep -q "quota"; then
            info "Using ext4 native quota feature..."
        fi
    fi

    # Add quota options to fstab if not present
    if ! grep -E "^\s*$root_dev|^\s*UUID=" /etc/fstab | grep -q 'usrquota'; then
        info "Adding quota options to /etc/fstab..."

        # Backup fstab
        cp /etc/fstab /etc/fstab.backup.$(date +%Y%m%d_%H%M%S)

        # Add quota options to root mount
        # Use # as delimiter since | is used as regex OR and $root_dev contains /
        sed -i -E "s#^([^#]*\s+/\s+\S+\s+)(\S+)#\1\2,usrquota,grpquota#" /etc/fstab

        # If sed didn't change anything (different fstab format), try another approach
        if ! grep ' / ' /etc/fstab | grep -q 'usrquota'; then
            # Try to add to defaults
            sed -i -E "s#^(\s*\S+\s+/\s+\S+\s+)defaults#\1defaults,usrquota,grpquota#" /etc/fstab
        fi
    fi

    # Remount with quota options
    info "Remounting root filesystem with quota options..."
    mount -o remount,usrquota,grpquota / 2>/dev/null || {
        warn "Could not remount with quotas - may require reboot"
    }

    # Check if quota tools work
    if mount | grep ' / ' | grep -q 'usrquota'; then
        # Create quota files if needed
        quotacheck -avugm 2>/dev/null || true

        # Enable quotas
        quotaon -avug 2>/dev/null || true

        if quotaon -p / 2>/dev/null | grep -q "is on"; then
            log "Disk quotas enabled successfully"
        else
            warn "Quotas configured but may require reboot to activate"
        fi
    else
        warn "Quota options not applied - may require reboot"
    fi
}

# Configure Redis with ACL
configure_redis() {
    header "Configuring Redis"

    # Generate random admin password
    REDIS_ADMIN_PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)

    # Save credentials for later use
    cat > /root/.jabali_redis_credentials << CREDS
REDIS_ADMIN_PASSWORD=${REDIS_ADMIN_PASSWORD}
CREDS
    chmod 600 /root/.jabali_redis_credentials

    # Configure Redis with ACL
    cat > /etc/redis/redis.conf << 'REDIS_CONF'
# Redis Configuration for Jabali Panel

# Network
bind 127.0.0.1
port 6379
protected-mode yes

# General
daemonize yes
pidfile /var/run/redis/redis-server.pid
loglevel notice
logfile /var/log/redis/redis-server.log

# Persistence
save 900 1
save 300 10
save 60 10000
stop-writes-on-bgsave-error yes
rdbcompression yes
rdbchecksum yes
dbfilename dump.rdb
dir /var/lib/redis

# Memory management
maxmemory 256mb
maxmemory-policy allkeys-lru

# ACL - Disable default user, require authentication
aclfile /etc/redis/users.acl

# Clients
timeout 0
tcp-keepalive 300
REDIS_CONF

    # Create ACL file with admin user (no comments allowed in Redis 8 ACL files)
    cat > /etc/redis/users.acl << ACL_CONF
user default off
user jabali_admin on >${REDIS_ADMIN_PASSWORD} ~* &* +@all
ACL_CONF

    # Remove any comments or empty lines (Redis 8 doesn't allow them)
    sed -i '/^#/d; /^$/d' /etc/redis/users.acl

    chmod 640 /etc/redis/users.acl
    chown redis:redis /etc/redis/users.acl

    # Restart Redis
    systemctl restart redis-server

    # Check if Redis started successfully
    if ! systemctl is-active --quiet redis-server; then
        error "Redis failed to start. Check /var/log/redis/redis-server.log"
        exit 1
    fi

    log "Redis configured with ACL authentication"
}

# Setup Jabali Panel
setup_jabali() {
    header "Setting Up Jabali Panel"

    cd "$JABALI_DIR"

    # Load database credentials
    source /root/.jabali_db_credentials

    # Load Redis credentials
    source /root/.jabali_redis_credentials

    # Create .env file (always use heredoc to ensure correct DB/cache settings)
    cat > .env << ENV
APP_NAME=Jabali
APP_ENV=production
APP_KEY=
APP_DEBUG=false
APP_URL=https://$(hostname -I | awk '{print $1}'):${PANEL_PORT}

LOG_CHANNEL=stack
LOG_LEVEL=error

DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=jabali
DB_USERNAME=jabali
DB_PASSWORD=${DB_PASSWORD}

CACHE_DRIVER=redis
SESSION_DRIVER=redis
QUEUE_CONNECTION=redis

REDIS_HOST=127.0.0.1
REDIS_USERNAME=jabali_admin
REDIS_PASSWORD=${REDIS_ADMIN_PASSWORD}
REDIS_PORT=6379

MAIL_MAILER=smtp
MAIL_HOST=127.0.0.1
MAIL_PORT=25
MAIL_ENCRYPTION=null
MAIL_USERNAME=null
MAIL_PASSWORD=null
MAIL_FROM_ADDRESS=webmaster@${SERVER_HOSTNAME}
MAIL_FROM_NAME="Jabali Panel"

MAIL_BACKEND=${MAIL_BACKEND}

PANEL_PORT=${PANEL_PORT}
PANEL_HOSTNAME=${SERVER_HOSTNAME:-}
PANEL_TLS_CERT=/etc/ssl/jabali/panel.crt
PANEL_TLS_KEY=/etc/ssl/jabali/panel.key
ENV

    # Add PowerDNS API credentials if installed
    if [[ -f /etc/jabali/powerdns-api.conf ]]; then
        local pdns_key=$(grep POWERDNS_API_KEY /etc/jabali/powerdns-api.conf | cut -d= -f2-)
        cat >> .env <<PDNSENV
POWERDNS_API_URL=http://127.0.0.1:8081
POWERDNS_API_KEY=${pdns_key}
POWERDNS_SERVER_ID=localhost
PDNSENV
    fi

    # Ensure mail settings are correct (in case .env.example was used)
    sed -i "s/^MAIL_MAILER=.*/MAIL_MAILER=smtp/" .env
    sed -i "s/^MAIL_FROM_ADDRESS=.*/MAIL_FROM_ADDRESS=webmaster@${SERVER_HOSTNAME}/" .env

    # Install dependencies
    run_quiet "Installing Composer dependencies..." \
        env COMPOSER_ALLOW_SUPERUSER=1 composer install --no-dev --optimize-autoloader
    if [ ! -f "$JABALI_DIR/vendor/autoload.php" ]; then
        error "Composer install failed - vendor/autoload.php not found"
        exit 1
    fi

    # Set storage permissions BEFORE running artisan commands
    # This prevents files being created as root and becoming inaccessible to www-data
    log "Setting storage permissions..."
    mkdir -p "$JABALI_DIR/storage/logs"
    mkdir -p "$JABALI_DIR/storage/framework/cache/data"
    mkdir -p "$JABALI_DIR/storage/framework/sessions"
    mkdir -p "$JABALI_DIR/storage/framework/views"
    mkdir -p "$JABALI_DIR/storage/app/public"
    mkdir -p "$JABALI_DIR/bootstrap/cache"
    touch "$JABALI_DIR/storage/logs/laravel.log"
    chown -R www-data:www-data "$JABALI_DIR/storage"
    chown -R www-data:www-data "$JABALI_DIR/bootstrap/cache"
    chmod -R 775 "$JABALI_DIR/storage"
    chmod -R 775 "$JABALI_DIR/bootstrap/cache"

    # Generate app key
    php artisan key:generate --force

    # Run migrations
    php artisan migrate --force

    # Create storage symlink for public files (screenshots, etc.)
    php artisan storage:link --force 2>/dev/null || true

    # Publish and configure Livewire for large file uploads
    php artisan livewire:publish --config 2>/dev/null || php artisan vendor:publish --tag=livewire:config 2>/dev/null || true
    if [[ -f "$JABALI_DIR/config/livewire.php" ]]; then
        sed -i "s/'rules' => null,/'rules' => ['required', 'file', 'max:524288'],/" "$JABALI_DIR/config/livewire.php"
        sed -i "s/'max_upload_time' => 5,/'max_upload_time' => 15,/" "$JABALI_DIR/config/livewire.php"
    fi

    # Configure DNS settings with server hostname and IP
    local server_ip=$(hostname -I | awk '{print $1}')

    # Extract root domain from hostname (e.g., panel.example.com -> example.com)
    # Count the number of dots to determine if it's a subdomain
    local dot_count=$(echo "$SERVER_HOSTNAME" | tr -cd '.' | wc -c)
    if [[ $dot_count -gt 1 ]]; then
        # It's a subdomain - extract root domain (last two parts)
        local root_domain=$(echo "$SERVER_HOSTNAME" | awk -F. '{print $(NF-1)"."$NF}')
        local subdomain_part=$(echo "$SERVER_HOSTNAME" | sed "s/\.$root_domain$//")
        log "Detected subdomain installation: $subdomain_part.$root_domain"
    else
        # It's already a root domain
        local root_domain="$SERVER_HOSTNAME"
        local subdomain_part=""
    fi

    log "Configuring DNS settings for ${SERVER_HOSTNAME} (root: ${root_domain}, IP: ${server_ip})..."

    php artisan tinker --execute="
        use App\Models\DnsSetting;
        DnsSetting::set('ns1', 'ns1.${root_domain}');
        DnsSetting::set('ns2', 'ns2.${root_domain}');
        DnsSetting::set('ns1_ip', '${server_ip}');
        DnsSetting::set('ns2_ip', '${server_ip}');
        DnsSetting::set('default_ip', '${server_ip}');
        DnsSetting::set('admin_email', 'admin.${root_domain}');
        DnsSetting::set('default_ttl', '3600');
        DnsSetting::set('mail_hostname', 'mail.${root_domain}');
    " 2>/dev/null || true

    # Create DNS zone for root domain
    log "Creating DNS zone for ${root_domain}..."

    # Build records array - include subdomain A record if installing on subdomain
    local records_json="[
        {\"name\": \"@\", \"type\": \"NS\", \"content\": \"ns1.${root_domain}\", \"ttl\": 3600},
        {\"name\": \"@\", \"type\": \"NS\", \"content\": \"ns2.${root_domain}\", \"ttl\": 3600},
        {\"name\": \"@\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600},
        {\"name\": \"www\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600},
        {\"name\": \"ns1\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600},
        {\"name\": \"ns2\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600},
        {\"name\": \"mail\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600},
        {\"name\": \"@\", \"type\": \"MX\", \"content\": \"mail.${root_domain}\", \"ttl\": 3600, \"priority\": 10},
        {\"name\": \"@\", \"type\": \"TXT\", \"content\": \"v=spf1 mx a ~all\", \"ttl\": 3600},
        {\"name\": \"_dmarc\", \"type\": \"TXT\", \"content\": \"v=DMARC1; p=none; rua=mailto:postmaster@${root_domain}\", \"ttl\": 3600}"

    # Add subdomain A record if installing on a subdomain
    if [[ -n "$subdomain_part" ]]; then
        records_json="${records_json},
        {\"name\": \"${subdomain_part}\", \"type\": \"A\", \"content\": \"${server_ip}\", \"ttl\": 3600}"
    fi
    records_json="${records_json}]"

    # Sync DNS records via agent (may fail if agent not yet started - that's OK,
    # the zone file was already written directly above)
    php artisan tinker --execute="
        try {
            \$agent = new App\Services\Agent\AgentClient();
            \$agent->send('dns.sync_zone', [
                'domain' => '${root_domain}',
                'records' => json_decode('${records_json}', true),
                'ns1' => 'ns1.${root_domain}',
                'ns2' => 'ns2.${root_domain}',
                'admin_email' => 'admin.${root_domain}',
                'default_ip' => '${server_ip}',
                'default_ttl' => 3600,
            ]);
        } catch (Exception \$e) {
            // Agent may not be running yet during install
        }
    " > /dev/null 2>&1 || true

    # Build assets
    export NPM_CONFIG_CACHE="$JABALI_DIR/storage/npm-cache"
    export PUPPETEER_SKIP_DOWNLOAD=1
    export PUPPETEER_CACHE_DIR="$JABALI_DIR/storage/puppeteer-cache"
    export XDG_CACHE_HOME="$JABALI_DIR/storage/.cache"
    mkdir -p "$NPM_CONFIG_CACHE" "$PUPPETEER_CACHE_DIR" "$XDG_CACHE_HOME"
    chown -R "$JABALI_USER:www-data" "$NPM_CONFIG_CACHE" "$PUPPETEER_CACHE_DIR" "$XDG_CACHE_HOME"
    chmod -R 775 "$NPM_CONFIG_CACHE" "$PUPPETEER_CACHE_DIR" "$XDG_CACHE_HOME"
    mkdir -p "$JABALI_DIR/public/build" "$JABALI_DIR/node_modules"
    chown -R "$JABALI_USER:www-data" "$JABALI_DIR/public/build" "$JABALI_DIR/node_modules"
    chmod -R 775 "$JABALI_DIR/public/build" "$JABALI_DIR/node_modules"
    if command -v sudo &>/dev/null; then
        run_quiet "Installing Node.js dependencies..." \
            sudo -u "$JABALI_USER" -H env \
                NPM_CONFIG_CACHE="$NPM_CONFIG_CACHE" \
                PUPPETEER_SKIP_DOWNLOAD=1 \
                PUPPETEER_CACHE_DIR="$PUPPETEER_CACHE_DIR" \
                XDG_CACHE_HOME="$XDG_CACHE_HOME" \
                npm install
        run_quiet "Building frontend assets..." \
            sudo -u "$JABALI_USER" -H env \
                NPM_CONFIG_CACHE="$NPM_CONFIG_CACHE" \
                PUPPETEER_SKIP_DOWNLOAD=1 \
                PUPPETEER_CACHE_DIR="$PUPPETEER_CACHE_DIR" \
                XDG_CACHE_HOME="$XDG_CACHE_HOME" \
                npm run build
    else
        run_quiet "Installing Node.js dependencies..." npm install
        run_quiet "Building frontend assets..." npm run build
    fi

    # Final permissions - ensure everything is correct after all setup
    chown -R $JABALI_USER:www-data "$JABALI_DIR"
    chown -R www-data:www-data "$JABALI_DIR/storage"
    chown -R www-data:www-data "$JABALI_DIR/bootstrap/cache"
    chmod -R 775 "$JABALI_DIR/storage"
    chmod -R 775 "$JABALI_DIR/bootstrap/cache"

    # Set SQLite database permissions for mail server access (if mail server is installed)
    if [[ "$INSTALL_MAIL" == "true" ]] && [[ -f "$JABALI_DIR/database/database.sqlite" ]]; then
        info "Setting SQLite database permissions for mail server..."
        chmod 664 "$JABALI_DIR/database/database.sqlite"
        chmod 775 "$JABALI_DIR/database"
    fi

    # Create CLI symlink
    ln -sf "$JABALI_DIR/bin/jabali" /usr/local/bin/jabali
    chmod +x "$JABALI_DIR/bin/jabali"
    chmod +x "$JABALI_DIR/bin/jabali-agent"

    # Update version file
    cat > "$JABALI_DIR/VERSION" << EOF
VERSION=${JABALI_VERSION}
EOF
    chown $JABALI_USER:$JABALI_USER "$JABALI_DIR/VERSION"

    # Create agent directories
    mkdir -p /var/run/jabali
    mkdir -p /var/log/jabali
    chown $JABALI_USER:$JABALI_USER /var/run/jabali
    chown $JABALI_USER:$JABALI_USER /var/log/jabali

    # Create backup directories
    mkdir -p /var/backups/jabali
    mkdir -p /var/backups/jabali/cpanel-migrations
    mkdir -p /var/backups/jabali/whm-migrations
    mkdir -p /var/backups/jabali/directadmin-migrations
    chown -R $JABALI_USER:$JABALI_USER /var/backups/jabali
    chmod 755 /var/backups/jabali /var/backups/jabali/cpanel-migrations /var/backups/jabali/whm-migrations /var/backups/jabali/directadmin-migrations

    log "Jabali Panel setup complete"
}

# Create systemd service for agent
setup_agent_service() {
    header "Setting Up Jabali Agent Service"

    cat > /etc/systemd/system/jabali-agent.service << 'SERVICE'
[Unit]
Description=Jabali Panel Agent
After=network.target

[Service]
Type=simple
User=root
Group=root
ExecStart=/usr/bin/php /var/www/jabali/bin/jabali-agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SERVICE

    systemctl daemon-reload
    systemctl enable jabali-agent
    systemctl start jabali-agent

    log "Jabali Agent service configured"
}

setup_queue_service() {
    header "Setting Up Jabali Queue Worker"

    cat > /etc/systemd/system/jabali-queue.service << 'SERVICE'
[Unit]
Description=Jabali Queue Worker
After=network.target jabali-agent.service
Wants=jabali-agent.service

[Service]
Type=simple
User=www-data
Group=www-data
WorkingDirectory=/var/www/jabali
ExecStart=/usr/bin/php /var/www/jabali/artisan queue:work --sleep=3 --tries=1 --timeout=3600
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jabali-queue

[Install]
WantedBy=multi-user.target
SERVICE

    systemctl daemon-reload
    systemctl enable jabali-queue
    systemctl start jabali-queue

    log "Jabali Queue Worker service configured"
}

# Setup Laravel scheduler cron job
setup_scheduler_cron() {
    header "Setting Up Laravel Scheduler"

    # Create log directory if it doesn't exist
    mkdir -p "$JABALI_DIR/storage/logs"
    chown -R www-data:www-data "$JABALI_DIR/storage/logs"

    # Ensure crontab command is available
    if ! command -v crontab >/dev/null 2>&1; then
        warn "crontab command not found, installing cron package..."
        run_quiet "Installing cron..." env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq cron || true
    fi

    if ! command -v crontab >/dev/null 2>&1; then
        warn "Unable to configure scheduler: crontab command is still missing"
        return
    fi

    # Ensure cron service is enabled and running
    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable cron >/dev/null 2>&1 || warn "Failed to enable cron service"
        if ! systemctl start cron >/dev/null 2>&1; then
            warn "Failed to start cron service — scheduled tasks will not run"
        fi
    fi

    # Add cron job for Laravel scheduler as www-data (runs every minute)
    CRON_LINE="* * * * * cd $JABALI_DIR && php artisan schedule:run >> /dev/null 2>&1"

    # Add to www-data's crontab (not root) to avoid permission issues with log files
    if ! crontab -u www-data -l 2>/dev/null | grep -q "artisan schedule:run"; then
        (crontab -u www-data -l 2>/dev/null; echo "$CRON_LINE") | crontab -u www-data -
        log "Laravel scheduler cron job added"
    else
        log "Laravel scheduler cron job already exists"
    fi

    log "Laravel scheduler configured - SSL auto-renewal and backups will run automatically"
}

# Setup logrotate for user domain logs and Jabali services
setup_logrotate() {
    header "Setting Up Log Rotation"

    cat > /etc/logrotate.d/jabali-users << 'LOGROTATE'
# Logrotate configuration for Jabali Panel user domain logs
/home/*/domains/*/logs/*.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 0644 root root
    sharedscripts
    postrotate
        invoke-rc.d nginx rotate >/dev/null 2>&1
    endscript
}

# ModSecurity audit log (when enabled)
/var/log/nginx/modsec_audit.log /var/log/modsec_audit.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 0640 www-data adm
    sharedscripts
    postrotate
        invoke-rc.d nginx rotate >/dev/null 2>&1
    endscript
}

# Jabali application logs
/var/www/jabali/storage/logs/*.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 0640 www-data www-data
}
LOGROTATE

    log "Log rotation configured for Jabali logs (domains, ModSecurity, app logs)"
}

# Setup Restic backup repository
setup_restic() {
    header "Setting Up Restic Backup"

    if ! command -v restic &>/dev/null; then
        warn "Restic not found, skipping backup setup"
        return
    fi

    # Generate encryption password if not exists
    local password_file="/etc/jabali/restic-password"
    if [[ ! -f "$password_file" ]]; then
        mkdir -p /etc/jabali
        openssl rand -hex 32 > "$password_file"
        chmod 600 "$password_file"
        log "Restic password generated"
    fi

    # Create default local backup directory (repo is initialized on first backup)
    mkdir -p /var/backups/jabali/restic

    log "Restic backup configured (add a destination in the panel to start backing up)"
}

# Setup SSL certificates for panel and mail services
setup_panel_ssl() {
    header "Setting Up SSL Certificates for Services"

    # Get public IP
    local server_ip=$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || curl -s --max-time 5 https://ipv4.icanhazip.com 2>/dev/null || hostname -I | awk '{print $1}')
    server_ip=$(echo "$server_ip" | tr -d '[:space:]')

    # Try to issue Let's Encrypt cert for the panel hostname
    # Skip if a valid LE cert already exists (avoids rate limit on reinstalls)
    local le_cert="/etc/letsencrypt/live/${SERVER_HOSTNAME}/fullchain.pem"
    if [[ -f "$le_cert" ]] && openssl x509 -in "$le_cert" -noout -checkend 86400 2>/dev/null; then
        # Valid LE cert exists and won't expire in 24h — reuse it
        cp "$le_cert" /etc/ssl/jabali/panel.crt
        cp "/etc/letsencrypt/live/${SERVER_HOSTNAME}/privkey.pem" /etc/ssl/jabali/panel.key
        chmod 644 /etc/ssl/jabali/panel.crt
        chown root:www-data /etc/ssl/jabali/panel.key
        chmod 640 /etc/ssl/jabali/panel.key
        systemctl reload jabali-panel 2>/dev/null || true
        log "Panel SSL: Reusing existing Let's Encrypt certificate for $SERVER_HOSTNAME"
    else
        local panel_resolved=$(dig +short "$SERVER_HOSTNAME" 2>/dev/null | head -1)
        if [[ "$panel_resolved" == "$server_ip" ]]; then
            info "Attempting Let's Encrypt certificate for panel ($SERVER_HOSTNAME)..."
            if certbot certonly --webroot -w /var/www/html -d "$SERVER_HOSTNAME" --non-interactive --agree-tos --email "${ADMIN_EMAIL:-admin@$SERVER_HOSTNAME}" --keep-until-expiring 2>/dev/null; then
                cp /etc/letsencrypt/live/$SERVER_HOSTNAME/fullchain.pem /etc/ssl/jabali/panel.crt
                cp /etc/letsencrypt/live/$SERVER_HOSTNAME/privkey.pem /etc/ssl/jabali/panel.key
                chmod 644 /etc/ssl/jabali/panel.crt
                chown root:www-data /etc/ssl/jabali/panel.key
                chmod 640 /etc/ssl/jabali/panel.key
                systemctl reload jabali-panel 2>/dev/null || true
                systemctl restart stalwart-mail 2>/dev/null || true
                log "Panel SSL: Let's Encrypt certificate issued for $SERVER_HOSTNAME"
            else
                info "Could not issue Let's Encrypt cert for panel — using self-signed"
            fi
        else
            info "Panel hostname does not resolve to this server — using self-signed certificate"
        fi
    fi

    # Certbot deploy hook: auto-copy renewed cert to FrankenPHP and reload
    mkdir -p /etc/letsencrypt/renewal-hooks/deploy
    cat > /etc/letsencrypt/renewal-hooks/deploy/jabali-panel.sh <<'DEPLOYHOOK'
#!/bin/bash
# Copy renewed panel cert to FrankenPHP cert path and reload
# Only copy if the renewed cert covers the panel hostname
PANEL_HOSTNAME=$(hostname -f 2>/dev/null || hostname)
PANEL_CERT="/etc/ssl/jabali/panel.crt"

if [ -f "$PANEL_CERT" ]; then
    for domain in $RENEWED_DOMAINS; do
        cert_path="/etc/letsencrypt/live/$domain/fullchain.pem"
        if [ -f "$cert_path" ]; then
            # Check if this cert covers the panel hostname
            if openssl x509 -in "$cert_path" -noout -text 2>/dev/null | grep -qF "DNS:$PANEL_HOSTNAME"; then
                cp "$cert_path" "$PANEL_CERT"
                cp "/etc/letsencrypt/live/$domain/privkey.pem" /etc/ssl/jabali/panel.key
                chmod 644 "$PANEL_CERT"
                chown root:www-data /etc/ssl/jabali/panel.key
                chmod 640 /etc/ssl/jabali/panel.key
                systemctl reload jabali-panel 2>/dev/null || true
                break
            fi
        fi
    done
    # Always restart Stalwart so it picks up any renewed cert
    systemctl restart stalwart-mail 2>/dev/null || true
fi
DEPLOYHOOK
    chmod 755 /etc/letsencrypt/renewal-hooks/deploy/jabali-panel.sh
    log "Certbot deploy hook installed for panel certificate renewal"

    # Try to issue certificate for mail hostname if different
    local mail_hostname="mail.$(echo "$SERVER_HOSTNAME" | awk -F. '{if(NF>2){for(i=2;i<=NF;i++)printf "%s%s",$i,(i<NF?".":"")}else print $0}')"

    if [[ "$mail_hostname" != "mail." && "$mail_hostname" != "$SERVER_HOSTNAME" ]]; then
        local mail_resolved=$(dig +short "$mail_hostname" 2>/dev/null | head -1)
        if [[ "$mail_resolved" == "$server_ip" ]]; then
            info "Attempting to issue Let's Encrypt certificate for $mail_hostname"

            # Create temporary nginx server block for mail hostname validation
            cat > /etc/nginx/sites-enabled/mail-cert-temp.conf << MAILNGINX
server {
    listen 80;
    server_name $mail_hostname;
    root /var/www/html;
}
MAILNGINX
            if nginx -t > /dev/null 2>&1; then
                systemctl reload nginx 2>/dev/null || warn "Failed to reload nginx for mail cert validation"
            fi

            if certbot certonly --webroot -w /var/www/html -d "$mail_hostname" --non-interactive --agree-tos --email "$ADMIN_EMAIL" 2>/dev/null; then
                log "Let's Encrypt certificate issued for $mail_hostname"

                # Configure Stalwart to use the LE cert
                local mail_cert="/etc/letsencrypt/live/$mail_hostname/fullchain.pem"
                local mail_key="/etc/letsencrypt/live/$mail_hostname/privkey.pem"
                cat > /etc/stalwart-mail/tls.toml <<TLSCONF
[certificate."default"]
cert = "file://${mail_cert}"
private-key = "file://${mail_key}"
default = true
TLSCONF
                systemctl restart stalwart-mail 2>/dev/null || true

                # Add certbot deploy hook to restart Stalwart on renewal
                mkdir -p /etc/letsencrypt/renewal-hooks/deploy
                cat > /etc/letsencrypt/renewal-hooks/deploy/stalwart-mail.sh <<'HOOK'
#!/bin/bash
if echo "$RENEWED_DOMAINS" | grep -q "mail\."; then
    systemctl restart stalwart-mail 2>/dev/null || true
fi
HOOK
                chmod +x /etc/letsencrypt/renewal-hooks/deploy/stalwart-mail.sh

                log "Stalwart TLS configured with Let's Encrypt certificate"
            else
                warn "Could not issue certificate for $mail_hostname"
            fi

            # Remove temporary nginx config for mail hostname
            rm -f /etc/nginx/sites-enabled/mail-cert-temp.conf
            if nginx -t > /dev/null 2>&1; then
                systemctl reload nginx 2>/dev/null || warn "Failed to reload nginx after mail cert cleanup"
            fi
        fi
    fi

    log "SSL setup complete"
}

# Setup self-healing services (automatic restart on failure)
setup_self_healing() {
    header "Setting Up Self-Healing Services"

    # List of critical services to harden with restart policies
    local services=(
        "jabali-panel"
        "nginx"
        "mariadb"
        "jabali-agent"
        "jabali-queue"
    )

    # Add PHP-FPM (detect version)
    for version in 8.5 8.4 8.3 8.2 8.1 8.0; do
        if systemctl list-unit-files "php${version}-fpm.service" &>/dev/null | grep -q "php${version}-fpm"; then
            services+=("php${version}-fpm")
            break
        fi
    done

    # Add optional services if installed
    if systemctl list-unit-files stalwart-mail.service &>/dev/null | grep -q stalwart-mail; then
        services+=("stalwart-mail")
    fi
    if systemctl list-unit-files bulwark.service &>/dev/null | grep -q bulwark; then
        services+=("bulwark")
    fi
    if systemctl list-unit-files pdns.service &>/dev/null | grep -q pdns; then
        services+=("pdns")
    fi
    if systemctl list-unit-files redis-server.service &>/dev/null | grep -q redis-server; then
        services+=("redis-server")
    fi

    # Create systemd override directory and restart policy for each service
    for service in "${services[@]}"; do
        local override_dir="/etc/systemd/system/${service}.service.d"
        mkdir -p "$override_dir"

        cat > "${override_dir}/restart.conf" << 'OVERRIDE'
[Unit]
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Restart=always
RestartSec=5
OVERRIDE

        log "Added restart policy for $service"
    done

    # Reload systemd to apply overrides
    systemctl daemon-reload

    # Setup health monitor service
    cat > /etc/systemd/system/jabali-health-monitor.service << 'SERVICE'
[Unit]
Description=Jabali Health Monitor - Automatic service recovery
Documentation=https://github.com/shukiv/jabali-panel
After=network.target jabali-agent.service
Wants=jabali-agent.service

[Service]
Type=simple
User=root
Group=root
ExecStart=/usr/bin/php /var/www/jabali/bin/jabali-health-monitor
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jabali-health-monitor

# Resource limits
LimitNOFILE=65535
MemoryMax=128M

[Install]
WantedBy=multi-user.target
SERVICE

    # Enable and start health monitor
    systemctl daemon-reload
    systemctl enable jabali-health-monitor
    systemctl start jabali-health-monitor

    log "Self-healing services configured"
    log "Health monitor running - check /var/log/jabali/health-monitor.log for events"
}

# Create admin user
create_admin() {
    header "Creating Admin User"

    cd "$JABALI_DIR"

    # Generate admin password and export for print_completion
    export ADMIN_PASSWORD=$(openssl rand -base64 16 | tr -dc 'a-zA-Z0-9' | head -c 16)

    php artisan tinker --execute="
        \$user = App\Models\User::updateOrCreate(
            ['username' => 'admin'],
            [
                'name' => 'Administrator',
                'email' => '${ADMIN_EMAIL}',
                'password' => bcrypt('${ADMIN_PASSWORD}'),
                'is_active' => true,
            ]
        );
        \$user->is_admin = true;
        \$user->save();
    " 2>/dev/null || true

    # Create Linux system user for admin (if not exists)
    if ! id admin &>/dev/null; then
        useradd -m -d /home/admin -s /usr/sbin/nologin admin 2>/dev/null || true
        usermod -aG sftpusers admin 2>/dev/null || true
    fi

    # Set Linux password so SFTP login works
    echo "admin:${ADMIN_PASSWORD}" | chpasswd 2>/dev/null || true

    # Save credentials
    echo "ADMIN_EMAIL=${ADMIN_EMAIL}" >> /root/.jabali_db_credentials
    echo "ADMIN_PASSWORD=${ADMIN_PASSWORD}" >> /root/.jabali_db_credentials
}

# Print completion message
print_completion() {
    local server_ip=$(hostname -I | awk '{print $1}')
    local admin_url="https://${SERVER_HOSTNAME}:${PANEL_PORT}/jabali-admin/"
    local user_url="https://${SERVER_HOSTNAME}:${PANEL_PORT}/jabali-panel/"

    echo ""
    echo ""
    echo -e "  ${GREEN}${BOLD}✓ Jabali Panel Installation Complete!${NC}"
    echo -e "  ${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  Version:    ${BOLD}v${JABALI_VERSION}${NC}"
    echo -e "  Hostname:   ${BOLD}${SERVER_HOSTNAME}${NC}"
    echo -e "  Server IP:  ${BOLD}${server_ip}${NC}"
    echo ""
    echo -e "  ${BOLD}Admin Credentials${NC}"
    echo -e "  Email:      ${CYAN}${ADMIN_EMAIL}${NC}"
    echo -e "  Password:   ${CYAN}${ADMIN_PASSWORD}${NC}"
    echo ""
    echo -e "  ${BOLD}Panel URLs${NC}"
    echo -e "  Admin:      ${CYAN}${admin_url}${NC}"
    echo -e "  User:       ${CYAN}${user_url}${NC}"

    if [[ -n "${WEBMASTER_EMAIL:-}" ]]; then
        echo ""
        echo -e "  ${BOLD}Webmaster Mailbox${NC}"
        echo -e "  Email:      ${CYAN}${WEBMASTER_EMAIL}${NC}"
        echo -e "  Password:   ${CYAN}${WEBMASTER_PASSWORD}${NC}"
        echo -e "  SMTP:       ${CYAN}mail.${SERVER_HOSTNAME}:587 (TLS)${NC}"
    fi

    if [[ "${INSTALL_DNS:-}" == "true" ]]; then
        echo ""
        echo -e "  ${BOLD}DNS Nameservers${NC}"
        echo -e "  ns1:        ${CYAN}ns1.${SERVER_HOSTNAME} -> ${server_ip}${NC}"
        echo -e "  ns2:        ${CYAN}ns2.${SERVER_HOSTNAME} -> ${server_ip}${NC}"
    fi

    echo ""
    echo -e "  ${BOLD}Quick Reference${NC}"
    echo -e "  CLI:        ${CYAN}jabali --help${NC}"
    echo -e "  Update:     ${CYAN}jabali update${NC}"
    echo -e "  Credentials:${CYAN} /root/.jabali_db_credentials${NC}"
    echo -e "  SSL:        Self-signed (or Let's Encrypt if hostname resolves)"

    # Show previous .env backup location if this was a re-install
    local latest_backup=$(ls -td /root/.jabali_reinstall_backup_* 2>/dev/null | head -1)
    if [[ -n "$latest_backup" ]]; then
        echo ""
        echo -e "  ${YELLOW}Backup:     ${latest_backup}${NC}"
    fi

    echo ""
    echo -e "  ${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

upgrade_stalwart() {
    local dpkg_arch
    dpkg_arch=$(dpkg --print-architecture)
    local rust_arch
    case "$dpkg_arch" in
        amd64)  rust_arch="x86_64" ;;
        arm64)  rust_arch="aarch64" ;;
        armhf)  rust_arch="armv7" ;;
        *)      warn "Unsupported architecture for Stalwart: $dpkg_arch"; return ;;
    esac

    local current_ver
    current_ver=$(/usr/local/bin/stalwart --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "unknown")

    # Get latest version from GitHub API
    local latest_ver
    latest_ver=$(curl -fsSL --connect-timeout 10 https://api.github.com/repos/stalwartlabs/mail-server/releases/latest 2>/dev/null | grep -oP '"tag_name":\s*"v?\K[0-9.]+' | head -1)

    if [[ -z "$latest_ver" ]]; then
        warn "Could not check Stalwart latest version"
        return
    fi

    if [[ "$current_ver" == "$latest_ver" ]]; then
        log "Stalwart v${current_ver} is already up to date"
        return
    fi

    info "Upgrading Stalwart from v${current_ver} to v${latest_ver}..."

    local tmp_dir
    tmp_dir=$(mktemp -d)
    local url="https://github.com/stalwartlabs/mail-server/releases/latest/download/stalwart-${rust_arch}-unknown-linux-gnu.tar.gz"

    if curl -fsSL "$url" -o "${tmp_dir}/stalwart.tar.gz" 2>/dev/null; then
        systemctl stop stalwart-mail 2>/dev/null || true
        tar -xzf "${tmp_dir}/stalwart.tar.gz" -C "${tmp_dir}"
        install -m 755 "${tmp_dir}/stalwart" /usr/local/bin/stalwart
        systemctl start stalwart-mail 2>/dev/null || true
        log "Stalwart upgraded to v${latest_ver}"
    else
        warn "Failed to download Stalwart v${latest_ver}"
    fi
    rm -rf "${tmp_dir}"
}

# Apply Jabali-specific patches to Bulwark source tree.
# Called during both initial install and upgrades (which reset to upstream HEAD).
# Must be run from inside the Bulwark directory.
patch_bulwark() {
    local bulwark_dir="${1:-/opt/bulwark}"
    cd "$bulwark_dir"

    # 1. basePath so Bulwark serves under /webmail/
    if ! grep -q 'basePath:' next.config.ts 2>/dev/null; then
        sed -i 's|output: "standalone",|output: "standalone",\n  basePath: "/webmail",|' next.config.ts
    fi

    # 2. localePrefix: 'always' to avoid rewrite loops in proxy mode
    sed -i "s|localePrefix: 'never'|localePrefix: 'always'|" i18n/routing.ts 2>/dev/null || true

    # 3. Rewrite proxy.ts to skip intl middleware for locale-prefixed paths
    cat > proxy.ts <<'PROXY_TS'
import { type NextRequest, NextResponse } from "next/server";
import createIntlMiddleware from "next-intl/middleware";
import { routing } from "./i18n/routing";

const intlMiddleware = createIntlMiddleware(routing);

function addSecurityHeaders(response: ReturnType<typeof NextResponse.next>, request: NextRequest) {
  const nonce = crypto.randomUUID();
  const isDev = process.env.NODE_ENV === "development";
  const scriptSrc = `'self' 'nonce-${nonce}' 'unsafe-eval'`;
  const connectSrc = isDev ? `'self' https: ws: wss:` : `'self' https:`;
  const csp = [
    `default-src 'self'`, `script-src ${scriptSrc}`,
    `style-src 'self' 'unsafe-inline'`, `img-src 'self' data: https:`,
    `font-src 'self'`, `connect-src ${connectSrc}`,
    `frame-src 'none'`, `object-src 'none'`,
    `base-uri 'self'`, `form-action 'self'`, `frame-ancestors 'none'`,
  ].join("; ");
  const existing = response.headers.get("x-middleware-override-headers");
  response.headers.set("x-middleware-override-headers", existing ? `${existing},x-nonce` : "x-nonce");
  response.headers.set("x-middleware-request-x-nonce", nonce);
  response.headers.set("X-Content-Type-Options", "nosniff");
  response.headers.set("X-Frame-Options", "DENY");
  response.headers.set("Referrer-Policy", "strict-origin-when-cross-origin");
  response.headers.set("X-XSS-Protection", "0");
  response.headers.set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()");
  response.headers.set("Content-Security-Policy-Report-Only", csp);
  return response;
}

export function proxy(request: NextRequest) {
  const pathname = request.nextUrl.pathname;
  const locales = routing.locales as readonly string[];
  const hasLocale = locales.some((l) => pathname === `/${l}` || pathname.startsWith(`/${l}/`));
  if (hasLocale) {
    return addSecurityHeaders(NextResponse.next(), request);
  }
  let intlResponse: ReturnType<typeof intlMiddleware> | null = null;
  try { intlResponse = intlMiddleware(request); } catch (error) { console.error("Locale middleware error:", error); }
  return addSecurityHeaders(intlResponse ?? NextResponse.next(), request);
}

export const config = {
  matcher: ["/", "/((?!api|_next|.*\\..*).*)" ],
};
PROXY_TS

    # 4. Fix client-side fetch calls to include basePath
    find hooks/ lib/ stores/ components/ -name '*.ts' -o -name '*.tsx' 2>/dev/null | \
        xargs grep -l "fetch('/api/" 2>/dev/null | \
        xargs -r sed -i "s|fetch('/api/|fetch('/webmail/api/|g"
    find components/ -name '*.tsx' 2>/dev/null | \
        xargs grep -l 'fetch("/api/' 2>/dev/null | \
        xargs -r sed -i 's|fetch("/api/|fetch("/webmail/api/|g'

    # 5. SSO API route — reads token file, POSTs to session endpoint internally,
    #    then redirects to /webmail/en with full session established.
    #    This avoids client-side JMAP connection issues (self-signed certs, etc.)
    mkdir -p app/api/auth/sso
    cat > app/api/auth/sso/route.ts <<'SSO_ROUTE'
import { NextRequest, NextResponse } from 'next/server';
import { readFileSync, unlinkSync, existsSync } from 'node:fs';
import { logger } from '@/lib/logger';

const SSO_TOKEN_DIR = '/var/lib/jabali/sso-tokens';

function getBaseUrl(request: NextRequest): string {
  const host = request.headers.get('x-forwarded-host') || request.headers.get('host') || 'localhost';
  const proto = request.headers.get('x-forwarded-proto') || 'https';
  return `${proto}://${host}`;
}

export async function GET(request: NextRequest) {
  const token = request.nextUrl.searchParams.get('token');
  const baseUrl = getBaseUrl(request);
  if (!token || !/^[a-f0-9]{64}$/.test(token)) {
    return NextResponse.redirect(`${baseUrl}/webmail/en/login`);
  }
  const tokenFile = `${SSO_TOKEN_DIR}/bulwark_sso_${token}`;
  try {
    if (!existsSync(tokenFile)) {
      return NextResponse.redirect(`${baseUrl}/webmail/en/login`);
    }
    const raw = readFileSync(tokenFile, 'utf-8');
    try { unlinkSync(tokenFile); } catch {}
    const data = JSON.parse(raw);
    if (!data.email || !data.expires || data.expires < Math.floor(Date.now() / 1000) || !data.password) {
      return NextResponse.redirect(`${baseUrl}/webmail/en/login`);
    }

    // POST to the session endpoint internally
    // Use baseUrl as serverUrl — the browser must be able to reach it for JMAP
    // (nginx proxies /jmap/* to Stalwart, so the external URL works)
    const jmapServerUrl = baseUrl || '';
    const sessionRes = await fetch(`http://127.0.0.1:${process.env.PORT || 3000}/webmail/api/auth/session`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Cookie': request.headers.get('cookie') || '',
      },
      body: JSON.stringify({
        serverUrl: jmapServerUrl,
        username: data.email,
        password: data.password,
      }),
    });

    if (!sessionRes.ok) {
      logger.error('SSO session POST failed', { status: sessionRes.status });
      return NextResponse.redirect(`${baseUrl}/webmail/en/login`);
    }

    // Forward the Set-Cookie headers from the session response
    const redirect = NextResponse.redirect(`${baseUrl}/webmail/en`);
    const setCookies = sessionRes.headers.getSetCookie();
    for (const cookie of setCookies) {
      redirect.headers.append('Set-Cookie', cookie);
    }

    logger.info('SSO login successful', { email: data.email });
    return redirect;
  } catch (error) {
    logger.error('SSO error', { error: error instanceof Error ? error.message : 'Unknown' });
    return NextResponse.redirect(`${baseUrl}/webmail/en/login`);
  }
}
SSO_ROUTE

    # 6. Auth-store patch: when checkAuth() finds zero accounts in the client-side
    # store, try to restore from an existing session cookie (set by SSO).
    # Without this, SSO sets cookies but the client-side store is empty so
    # checkAuth() sends the user to the login page.
    local auth_store="stores/auth-store.ts"
    if [[ -f "$auth_store" ]]; then
        # Find the line "// Legacy single-account fallback" and inject SSO cookie
        # restoration block before it, inside the `if (accounts.length > 0)` else branch.
        python3 -c "
import sys
content = open('$auth_store').read()

# The injection point: right after the accounts.length > 0 block ends and before
# the legacy fallback. We look for the pattern where accounts is empty.
marker = '// Legacy single-account fallback'
if marker not in content:
    print('auth-store: marker not found, skipping patch', file=sys.stderr)
    sys.exit(0)

# Check if already patched
if 'SSO cookie restoration' in content:
    print('auth-store: already patched', file=sys.stderr)
    sys.exit(0)

patch = '''
        // SSO cookie restoration: if no accounts are registered but a session
        // cookie exists (e.g. from panel SSO), try to restore from the cookie.
        try {
          const ssoRes = await fetch('/webmail/api/auth/session?slot=0', {
            method: 'PUT',
            credentials: 'same-origin',
          });
          if (ssoRes.ok) {
            const { serverUrl: ssoServerUrl, username: ssoUsername, password: ssoPassword } = await ssoRes.json();
            if (ssoServerUrl && ssoUsername && ssoPassword) {
              // Use the store's own login method so the account is properly registered
              await get().login(ssoServerUrl, ssoUsername, ssoPassword, undefined, true);
              return;
            }
          }
        } catch (ssoErr) {
          // SSO restoration failed — fall through to normal flow
        }

        '''

content = content.replace(marker, patch + marker)
open('$auth_store', 'w').write(content)
print('auth-store: SSO cookie restoration patch applied')
" 2>&1 || true
    fi

    log "Bulwark patches applied (basePath, SSO, auth-store)"
}

upgrade_bulwark() {
    local bulwark_dir="/opt/bulwark"
    # Bump this when Jabali patches change to force a rebuild even without upstream changes
    local jabali_patch_version="11"

    if ! command -v node >/dev/null 2>&1; then
        warn "Node.js not available — skipping Bulwark update"
        return
    fi

    cd "$bulwark_dir"

    # Check if upstream has updates
    git fetch --depth 1 origin main 2>/dev/null || { warn "Could not fetch Bulwark updates"; return; }
    local local_hash remote_hash
    local_hash=$(git rev-parse HEAD 2>/dev/null)
    remote_hash=$(git rev-parse origin/main 2>/dev/null)

    local needs_rebuild=false
    if [[ "$local_hash" != "$remote_hash" ]]; then
        needs_rebuild=true
        info "Updating Bulwark Webmail (upstream change)..."
        git reset --hard origin/main 2>/dev/null || { warn "Could not update Bulwark"; return; }
    fi

    # Also rebuild if our patch version changed (e.g. SSO fix)
    local current_patch_ver=""
    [[ -f .jabali-patch-version ]] && current_patch_ver=$(cat .jabali-patch-version 2>/dev/null)
    if [[ "$current_patch_ver" != "$jabali_patch_version" ]]; then
        needs_rebuild=true
        info "Re-patching Bulwark (patch version ${current_patch_ver:-0} → ${jabali_patch_version})..."
        # Reset to clean upstream state before re-patching
        git checkout -- . 2>/dev/null || true
    fi

    # Migrate JMAP_SERVER_URL to local Stalwart if still pointing through nginx
    if [[ -f "${bulwark_dir}/.env.local" ]]; then
        if grep -q 'JMAP_SERVER_URL=https://' "${bulwark_dir}/.env.local" 2>/dev/null; then
            sed -i 's|JMAP_SERVER_URL=https://.*|JMAP_SERVER_URL=http://127.0.0.1:8090|' "${bulwark_dir}/.env.local"
            systemctl restart bulwark 2>/dev/null || true
            info "Migrated JMAP_SERVER_URL to local Stalwart"
        fi
    fi

    if [[ "$needs_rebuild" != "true" ]]; then
        log "Bulwark Webmail is already up to date"
        return
    fi

    # Re-apply Jabali patches (basePath, SSO route, auth-store, etc.)
    patch_bulwark "$bulwark_dir"

    npm ci --ignore-scripts 2>/dev/null || npm install --ignore-scripts 2>/dev/null || { warn "Bulwark npm install failed"; return; }
    npm run build 2>/dev/null || { warn "Bulwark build failed"; return; }

    # Re-link static assets for standalone mode
    ln -sfn "${bulwark_dir}/.next/static" "${bulwark_dir}/.next/standalone/.next/static"
    ln -sfn "${bulwark_dir}/public" "${bulwark_dir}/.next/standalone/public"
    ln -sfn "${bulwark_dir}/.env.local" "${bulwark_dir}/.next/standalone/.env.local"
    chown -R www-data:www-data "${bulwark_dir}/.next" "${bulwark_dir}/.env.local"

    # Only mark patch version after successful build
    echo "$jabali_patch_version" > .jabali-patch-version

    systemctl restart bulwark 2>/dev/null || true
    log "Bulwark Webmail updated"
}

# Upgrade Jabali Panel infrastructure (called by: jabali update)
# Re-runs safe config functions without wiping data.
# Skips: mariadb (would drop user), redis (would regen password), firewall (would reset rules)
upgrade_infra() {
    check_root

    header "Upgrading Jabali Panel Infrastructure"

    # Detect PHP version
    if [[ -z "${PHP_VERSION:-}" ]]; then
        detect_php_version
    fi

    # Install default suspended page if not customized
    mkdir -p /etc/jabali
    if [[ ! -f /etc/jabali/suspended.html ]]; then
        cp "$JABALI_DIR/stubs/suspended.html" /etc/jabali/suspended.html
        info "Installed default suspended page"
    fi

    # Fix FPM pool configs with empty pm= (bug: createFpmPool missed $pmType before v0.9.x)
    for pool_conf in /etc/php/*/fpm/pool.d/*.conf; do
        [[ -f "$pool_conf" ]] || continue
        if grep -qE '^pm\s*=\s*$' "$pool_conf"; then
            sed -i 's/^pm\s*=\s*$/pm = dynamic/' "$pool_conf"
            info "Fixed empty pm= in $(basename "$pool_conf")"
        fi
    done

    # Re-run safe configuration functions
    header "Updating PHP Configuration"
    configure_php

    header "Updating Nginx Configuration"
    configure_nginx

    # Add JMAP proxy block to existing domain vhosts if missing
    for vhost in /etc/nginx/sites-available/*.conf; do
        [[ -f "$vhost" ]] || continue
        if grep -q 'location.*webmail' "$vhost" && ! grep -q 'location.*\^~.*/jmap/' "$vhost"; then
            domain_name=$(basename "$vhost" .conf)
            python3 -c "
import sys
with open(sys.argv[1]) as f:
    content = f.read()
block = '''
    location = /.well-known/jmap {
        return 301 /jmap/session;
    }

    location ^~ /jmap/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \"upgrade\";
        sub_filter_types application/json;
        sub_filter_once off;
    }

'''
content = content.replace('    location = /webmail', block + '    location = /webmail', 1)
with open(sys.argv[1], 'w') as f:
    f.write(content)
" "$vhost"
            info "Added JMAP proxy to $domain_name"
        fi
    done
    nginx -t 2>/dev/null && nginx -s reload 2>/dev/null || true

    header "Updating FrankenPHP"
    install_frankenphp

    header "Updating Systemd Services"
    setup_agent_service
    setup_frankenphp_config
    setup_panel_service
    setup_queue_service
    setup_scheduler_cron
    setup_logrotate
    setup_restic
    setup_self_healing

    # Fix waf.conf if it uses modsecurity directive without the module loaded
    local waf_conf="/etc/nginx/jabali/includes/waf.conf"
    if [[ -f "$waf_conf" ]] && grep -q "^modsecurity" "$waf_conf" && ! nginx -V 2>&1 | grep -q modsecurity; then
        echo "# Managed by Jabali — jabali-security will configure ModSecurity here" > "$waf_conf"
        info "Fixed waf.conf (modsecurity module not loaded)"
    fi

    # Install/update jabali-isolator (nspawn containers for PHP-FPM + shell)
    install_jabali_isolator

    # Configure SSH + jabali-shell
    configure_sshd
    install_jabali_shell

    # Migrate old jail-based shell users to nspawn (one-time)
    if [[ -d /var/jail ]] && getent group shellusers &>/dev/null; then
        header "Migrating Shell Users to nspawn"
        # Agent handles the actual migration — call via socket if running
        if systemctl is-active --quiet jabali-agent 2>/dev/null; then
            local response
            response=$(curl -s --unix-socket /var/run/jabali-agent.sock \
                -X POST -H "Content-Type: application/json" \
                -d '{"action":"ssh.migrate_shell_users","params":{}}' \
                http://localhost/rpc 2>/dev/null) || true
            if echo "$response" | grep -q '"success":true'; then
                info "Shell user migration complete"
            else
                warn "Shell user migration via agent failed (will retry on next update)"
            fi
        else
            warn "Agent not running — shell user migration will run on next update"
        fi
    fi

    # Update Stalwart Mail Server (binary from GitHub releases)
    if [[ -x /usr/local/bin/stalwart ]]; then
        header "Updating Stalwart Mail Server"
        upgrade_stalwart
    fi

    # Update Bulwark Webmail (Next.js app from GitHub)
    if [[ -d /opt/bulwark/.git ]]; then
        header "Updating Bulwark Webmail"
        upgrade_bulwark
    fi

    # Update WP-CLI
    if command -v wp &>/dev/null; then
        wp cli update --yes 2>/dev/null || true
    fi

    # Update Composer
    if command -v composer &>/dev/null; then
        composer self-update --quiet 2>/dev/null || true
    fi

    # Install or update jabali-security
    if command -v jabali-security &>/dev/null; then
        header "Updating Jabali Security"
        jabali-security update || warn "jabali-security update failed (non-fatal)"
    else
        install_jabali_security
    fi

    # Patch existing vhosts: add Bulwark static file serving if missing
    if [[ -d /opt/bulwark/.next/static ]]; then
        for vhost in /etc/nginx/sites-available/*; do
            [[ -f "$vhost" ]] || continue
            if grep -q "location.*webmail/" "$vhost" 2>/dev/null && ! grep -q "webmail/_next/static" "$vhost" 2>/dev/null; then
                sed -i '/location \^~ \/webmail\//i\
    location ^~ /webmail/_next/static/ {\
        alias /opt/bulwark/.next/static/;\
        expires 365d;\
        add_header Cache-Control "public, max-age=31536000, immutable";\
        access_log off;\
    }\
' "$vhost"
                info "Patched $(basename "$vhost"): added webmail static file serving"
            fi
        done
    fi

    # Restart services to pick up changes
    header "Restarting Services"
    if [[ "${JABALI_SKIP_AGENT_RESTART:-}" != "1" ]]; then
        systemctl restart jabali-agent 2>/dev/null || true
    else
        log "Skipping agent restart (called from agent)"
    fi
    systemctl restart jabali-queue 2>/dev/null || true
    systemctl restart jabali-panel 2>/dev/null || true
    systemctl restart "php${PHP_VERSION}-fpm" 2>/dev/null || true
    systemctl reload nginx 2>/dev/null || true

    # Fix .git ownership so both root CLI and www-data panel can use it
    chown -R www-data:www-data "$JABALI_DIR/.git" 2>/dev/null || true

    log "Infrastructure upgrade complete"
}

reinstall() {
    show_banner
    check_root

    header "Jabali Panel Reinstall"

    if [[ ! -d "/var/www/jabali" ]]; then
        error "Jabali Panel is not installed. Use 'install' instead."
        exit 1
    fi

    echo ""
    echo -e "${YELLOW}This will regenerate ALL configurations and reset the database.${NC}"
    echo -e "${YELLOW}Packages (nginx, php, mariadb, etc.) will NOT be reinstalled.${NC}"
    echo ""

    # Back up .env and credentials
    local backup_dir="/root/.jabali_reinstall_backup_$(date +%Y%m%d_%H%M%S)"
    mkdir -p "$backup_dir"
    cp /var/www/jabali/.env "$backup_dir/.env" 2>/dev/null || true
    cp /root/.jabali_db_credentials "$backup_dir/db_credentials" 2>/dev/null || true
    cp /root/.jabali_redis_credentials "$backup_dir/redis_credentials" 2>/dev/null || true
    info "Backed up .env and credentials to $backup_dir"

    # Detect PHP version
    detect_php_version

    # Prompt for hostname
    prompt_hostname
    select_features

    # Stop services during reinstall
    systemctl stop jabali-agent 2>/dev/null || true
    systemctl stop jabali-queue 2>/dev/null || true
    systemctl stop jabali-panel 2>/dev/null || true

    # Re-run all configuration functions (packages already installed)
    configure_php
    configure_mariadb
    configure_nginx

    if [[ "$INSTALL_MAIL" == "true" ]]; then
        configure_stalwart
    fi

    if [[ "$INSTALL_DNS" == "true" ]]; then
        configure_dns
    fi

    setup_quotas
    configure_redis

    # Regenerate .env, run migrations, create admin
    setup_jabali
    setup_agent_service
    setup_frankenphp_config
    setup_panel_service
    setup_queue_service
    setup_scheduler_cron
    setup_logrotate
    setup_restic
    setup_panel_ssl
    setup_self_healing
    create_admin
    create_webmaster_mailbox
    configure_ssh_notifications

    # Fix .git ownership so both root CLI and www-data panel can use it
    chown -R www-data:www-data "$JABALI_DIR/.git" 2>/dev/null || true

    # Restart all services
    header "Restarting Services"
    systemctl restart jabali-agent 2>/dev/null || true
    systemctl restart jabali-queue 2>/dev/null || true
    systemctl restart jabali-panel 2>/dev/null || true
    systemctl restart "php${PHP_VERSION}-fpm" 2>/dev/null || true
    systemctl reload nginx 2>/dev/null || true

    print_completion
    info "Previous config backed up at: $backup_dir"
}

# Uninstall Jabali Panel
uninstall() {
    local force_uninstall=false
    local keep_packages="${UNINSTALL_KEEP_PACKAGES:-false}"

    # Check for flags
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --force|-f) force_uninstall=true; shift ;;
            --keep-packages|--keep) keep_packages=true; shift ;;
            *) shift ;;
        esac
    done

    show_banner
    check_root

    # Detect PHP version for service names
    if command -v php &>/dev/null; then
        PHP_VERSION=$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;' 2>/dev/null)
    fi
    PHP_VERSION="${PHP_VERSION:-8.5}"

    if [[ "$keep_packages" == "true" ]]; then
        echo -e "${YELLOW}${BOLD}This will remove Jabali Panel files and settings (packages will be kept).${NC}"
        echo ""
        echo "This will remove:"
        echo "  - Jabali Panel files (/var/www/jabali)"
        echo "  - Jabali database and user"
        echo "  - Jabali services and configs"
    else
        echo -e "${RED}${BOLD}WARNING: This will completely remove Jabali Panel and all related services!${NC}"
        echo ""
        echo "This will remove:"
        echo "  - Jabali Panel files (/var/www/jabali)"
        echo "  - Jabali database and user"
        echo "  - Nginx, PHP-FPM, MariaDB, Redis"
        echo "  - Mail server (Stalwart)"
        echo "  - DNS server (PowerDNS)"
        echo "  - All user home directories (/home/*)"
        echo "  - All virtual mail (/var/mail)"
        echo "  - All domains and configurations"
    fi
    echo ""
    echo -e "${YELLOW}This action cannot be undone!${NC}"
    echo ""

    if [[ "$force_uninstall" == "false" ]]; then
        # First confirmation
        read -p "Are you sure you want to uninstall? (y/N): " confirm1 < /dev/tty
        if [[ ! "$confirm1" =~ ^[Yy]$ ]]; then
            info "Uninstall cancelled"
            exit 0
        fi

        echo ""
        echo -e "${RED}${BOLD}FINAL WARNING: ALL DATA WILL BE PERMANENTLY DELETED!${NC}"
        echo ""

        # Second confirmation - require typing
        read -p "Type 'YES DELETE EVERYTHING' to confirm: " confirm2 < /dev/tty
        if [[ "$confirm2" != "YES DELETE EVERYTHING" ]]; then
            info "Uninstall cancelled"
            exit 0
        fi
    else
        warn "Force mode enabled - skipping confirmations"
    fi

    header "Stopping Services"
    systemctl stop jabali-agent 2>/dev/null || true
    systemctl disable jabali-agent 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-agent.service
    rm -rf /etc/systemd/system/jabali-agent.service.d

    systemctl stop jabali-health-monitor 2>/dev/null || true
    systemctl disable jabali-health-monitor 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-health-monitor.service
    rm -rf /etc/systemd/system/jabali-health-monitor.service.d

    systemctl stop jabali-queue 2>/dev/null || true
    systemctl disable jabali-queue 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-queue.service
    rm -rf /etc/systemd/system/jabali-queue.service.d

    systemctl stop jabali-panel 2>/dev/null || true
    systemctl disable jabali-panel 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-panel.service
    rm -rf /etc/systemd/system/jabali-panel.service.d

    # Stop and remove Stalwart Mail Server
    systemctl stop stalwart-mail 2>/dev/null || true
    systemctl disable stalwart-mail 2>/dev/null || true
    rm -f /etc/systemd/system/stalwart-mail.service

    # Stop and remove Bulwark Webmail
    systemctl stop bulwark 2>/dev/null || true
    systemctl disable bulwark 2>/dev/null || true
    rm -f /etc/systemd/system/bulwark.service

    # Stop and remove jabali-security
    if command -v jabali-security &>/dev/null; then
        jabali-security uninstall --force 2>/dev/null || true
    fi
    systemctl stop jabali-security 2>/dev/null || true
    systemctl disable jabali-security 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-security.service

    # Stop and remove nspawn containers (jabali-isolator)
    if command -v jabali-isolate &>/dev/null; then
        for machine_dir in /var/lib/machines/*-php; do
            [[ -d "$machine_dir" ]] || continue
            local container_name
            container_name=$(basename "$machine_dir")
            local user_name="${container_name%-php}"
            jabali-isolate destroy "$user_name" 2>/dev/null || true
        done
    fi

    # Stop idle container timer
    systemctl stop jabali-container-idle-check.timer 2>/dev/null || true
    systemctl disable jabali-container-idle-check.timer 2>/dev/null || true
    rm -f /etc/systemd/system/jabali-container-idle-check.timer
    rm -f /etc/systemd/system/jabali-container-idle-check.service

    local services=(
        nginx
        php-fpm
        php${PHP_VERSION}-fpm
        mariadb
        mysql
        redis-server
        stalwart-mail
        bulwark
        postfix
        dovecot
        rspamd
        opendkim
        pdns
    )

    for service in "${services[@]}"; do
        systemctl stop "$service" 2>/dev/null || true
        systemctl disable "$service" 2>/dev/null || true
    done

    # Remove systemd restart overrides
    rm -rf /etc/systemd/system/nginx.service.d/restart.conf
    rm -rf /etc/systemd/system/mariadb.service.d/restart.conf
    rm -rf /etc/systemd/system/jabali-agent.service.d/restart.conf
    rm -rf /etc/systemd/system/jabali-queue.service.d/restart.conf
    rm -rf /etc/systemd/system/php*.service.d/restart.conf
    rm -rf /etc/systemd/system/postfix.service.d/restart.conf
    rm -rf /etc/systemd/system/dovecot.service.d/restart.conf
    rm -rf /etc/systemd/system/pdns.service.d/restart.conf
    rm -rf /etc/systemd/system/redis-server.service.d/restart.conf
    rm -rf /etc/systemd/system/stalwart-mail.service.d
    rm -rf /etc/systemd/system/bulwark.service.d
    rm -rf /etc/systemd/system/systemd-nspawn@*-php.service.d

    systemctl daemon-reload

    header "Removing Jabali Panel"
    rm -rf "$JABALI_DIR"
    rm -rf /var/www/jabali.bk
    rm -rf /var/www/jabali.bak.*
    rm -f /usr/local/bin/jabali
    rm -f /usr/local/bin/frankenphp
    rm -rf /etc/frankenphp
    rm -f /etc/letsencrypt/renewal-hooks/deploy/jabali-panel.sh
    rm -rf /var/lib/jabali
    rm -rf /var/run/jabali
    rm -rf /var/log/jabali
    rm -rf /var/backups/jabali
    rm -rf /etc/jabali
    rm -rf /etc/ssl/jabali
    rm -f /etc/security/jabali-ssh-notify.sh
    rm -f /usr/local/bin/wp
    rm -f /root/.jabali_db_credentials
    rm -f /root/.jabali_redis_credentials
    rm -f /root/jabali_credentials.txt
    rm -f /etc/powerdns/pdns.d/jabali.conf
    rm -f /etc/needrestart/conf.d/99-jabali.conf
    rm -f /etc/systemd/resolved.conf.d/jabali.conf
    rm -rf /root/.jabali_reinstall_backup_*
    log "Jabali Panel removed"

    header "Removing Database"
    mysql -e "DROP DATABASE IF EXISTS jabali;" 2>/dev/null || true
    mysql -e "DROP USER IF EXISTS 'jabali'@'localhost';" 2>/dev/null || true
    mysql -e "DROP USER IF EXISTS 'jabali'@'127.0.0.1';" 2>/dev/null || true
    log "Database removed"

    if [[ "$keep_packages" == "true" ]]; then
        log "Keeping packages installed (--keep-packages)"
    else

    header "Removing Packages"

    # Set non-interactive mode to prevent dialog prompts
    export DEBIAN_FRONTEND=noninteractive

    # Pre-configure debconf to remove databases without prompting
    echo "mariadb-server mysql-server/remove-data-dir boolean true" | debconf-set-selections 2>/dev/null || true
    echo "mariadb-server-10.5 mysql-server/remove-data-dir boolean true" | debconf-set-selections 2>/dev/null || true
    echo "mariadb-server-10.6 mysql-server/remove-data-dir boolean true" | debconf-set-selections 2>/dev/null || true
    echo "mariadb-server-10.11 mysql-server/remove-data-dir boolean true" | debconf-set-selections 2>/dev/null || true

    local packages=(
        # Web Server
        nginx
        nginx-common

        # PHP
        'php*'

        # Database
        mariadb-server
        mariadb-client
        mariadb-common

        # Cache
        redis-server

        # Mail Server
        postfix
        postfix-mysql
        dovecot-core
        dovecot-imapd
        dovecot-pop3d
        dovecot-lmtpd
        dovecot-mysql
        opendkim
        opendkim-tools
        rspamd

        # DNS
        pdns-server
        pdns-backend-mysql

        # Webmail
        roundcube
        roundcube-core
        roundcube-mysql
        roundcube-sqlite3
        roundcube-plugins

        # phpMyAdmin
        phpmyadmin

        # Additional components installed by Jabali
        nodejs
        geoipupdate
        libnginx-mod-http-geoip2
        chromium
        build-essential
        quota
        goaccess
        sqlite3
    )

    if [[ "$DEBUG" == "true" ]]; then
        for pkg in "${packages[@]}"; do
            DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq $pkg 2>/dev/null || true
        done
    else
        info "Removing packages..."
        for pkg in "${packages[@]}"; do
            DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq $pkg > /dev/null 2>&1 || true
        done
        log "Packages removed"
    fi

    run_quiet "Cleaning up..." \
        bash -c 'DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>&1; DEBIAN_FRONTEND=noninteractive apt-get autoclean -y -qq 2>&1; true'

    header "Cleaning Up Files"
    # Web server
    rm -rf /etc/nginx
    rm -rf /var/cache/nginx
    rm -rf /etc/php
    rm -rf /var/lib/php
    rm -rf /var/log/nginx

    # Database
    rm -rf /var/lib/mysql
    rm -rf /var/lib/redis
    rm -rf /var/log/mysql

    # Mail server
    rm -rf /etc/postfix
    rm -rf /etc/dovecot
    rm -rf /etc/opendkim
    rm -rf /etc/rspamd
    rm -rf /var/mail
    rm -rf /var/vmail
    rm -rf /var/spool/postfix
    rm -rf /var/log/mail.*

    # DNS
    rm -rf /etc/bind
    rm -rf /var/cache/bind
    rm -rf /var/log/named

    # Webmail (Roundcube)
    rm -rf /etc/roundcube
    rm -rf /var/lib/roundcube
    rm -rf /var/log/roundcube
    rm -rf /usr/share/roundcube

    # phpMyAdmin
    rm -rf /etc/phpmyadmin
    rm -rf /usr/share/phpmyadmin

    # GeoIP
    rm -rf /etc/geoipupdate
    rm -rf /etc/GeoIP.conf
    rm -rf /usr/share/GeoIP
    rm -rf /var/lib/GeoIP
    rm -f /usr/local/bin/geoipupdate

    # Stalwart Mail Server
    rm -f /usr/local/bin/stalwart
    rm -rf /etc/stalwart-mail
    rm -rf /var/lib/stalwart-mail
    rm -rf /var/log/stalwart-mail

    # Bulwark Webmail
    rm -rf /opt/bulwark

    # jabali-security
    rm -rf /usr/local/jabali-security
    rm -rf /etc/jabali-security
    rm -f /usr/local/bin/jabali-security

    # jabali-isolator
    rm -rf /usr/local/jabali-isolator
    rm -f /usr/local/bin/jabali-isolate
    rm -f /usr/local/bin/jabali-container-idle-check
    rm -rf /var/lib/machines/*-php
    rm -rf /etc/systemd/nspawn/*-php.nspawn
    rm -rf /run/jabali-isolator
    rm -rf /run/jabali-fpm

    # SSL certificates (Let's Encrypt)
    rm -rf /etc/letsencrypt

    # PHP repository
    rm -f /etc/apt/sources.list.d/php.list
    rm -f /usr/share/keyrings/sury-php.gpg
    # Nginx repository
    rm -f /etc/apt/sources.list.d/nginx.list
    rm -f /usr/share/keyrings/sury-nginx.gpg
    # NodeSource repository
    rm -f /etc/apt/sources.list.d/nodesource.list
    rm -f /etc/apt/sources.list.d/nodesource.sources
    rm -f /usr/share/keyrings/nodesource.gpg
    # imapsync
    rm -f /usr/local/bin/imapsync
    # Jabali contrib repository
    rm -f /etc/apt/sources.list.d/jabali-contrib.list

    fi  # end of keep_packages check

    # jabali-shell and sudoers
    rm -f /usr/local/bin/jabali-shell
    rm -f /usr/local/bin/jabali-shell-bwrap
    rm -f /etc/sudoers.d/jabali-shell
    rm -f /etc/polkit-1/rules.d/50-jabali-shell.rules
    sed -i '\|/usr/local/bin/jabali-shell|d' /etc/shells 2>/dev/null || true

    # Jabali-specific configs
    rm -rf /var/backups/users
    rm -f /etc/logrotate.d/jabali-users

    # Remove www-data cron jobs (Laravel scheduler)
    if command -v crontab >/dev/null 2>&1; then
        crontab -u www-data -r 2>/dev/null || true
    fi

    log "Configuration files cleaned"

    header "Removing User Data"
    # Always prompt — even in force mode, home directory deletion requires explicit consent
    echo -n "Remove all user home directories? (y/N): "
    read remove_homes < /dev/tty 2>/dev/null || remove_homes="n"
    if [[ "$remove_homes" =~ ^[Yy]$ ]]; then
        # Get list of normal users (UID >= 1000, excluding nobody)
        for user_home in /home/*; do
            if [[ -d "$user_home" ]]; then
                username=$(basename "$user_home")
                userdel -r "$username" 2>/dev/null || rm -rf "$user_home"
                log "Removed user: $username"
            fi
        done
    else
        info "Keeping user home directories"
    fi

    # Remove vmail user
    userdel vmail 2>/dev/null || true
    groupdel vmail 2>/dev/null || true

    echo ""
    echo ""
    echo -e "  ${GREEN}${BOLD}✓ Jabali Panel Uninstallation Complete!${NC}"
    echo -e "  ${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo -e "  All Jabali Panel components have been removed."
    echo -e "  Your server is now clean."
    echo ""
}

# Show usage
show_usage() {
    echo "Jabali Panel Installer"
    echo ""
    echo "Usage: $0 [command] [options]"
    echo ""
    echo "Options:"
    echo "  --git <url>          Use a custom git repository URL"
    echo "  --branch <name>     Clone a specific branch (default: main)"
    echo "  --debug              Show verbose output (apt, npm, composer, etc.)"
    echo ""
    echo "Commands:"
    echo "  install              Install Jabali Panel (default, interactive)"
    echo "  reinstall            Reset all configs and database (keeps packages)"
    echo "  upgrade              Re-apply infrastructure configs (nginx, php, systemd)"
    echo "  uninstall [--force]  Remove Jabali Panel and all components"
    echo "  uninstall --keep     Remove Jabali only (keep packages like nginx, php, etc.)"
    echo "  --help               Show this help message"
    echo ""
    echo "Environment Variables (for non-interactive install):"
    echo "  SERVER_HOSTNAME      Set the server hostname"
    echo "  JABALI_FULL          Install all components (set to any value)"
    echo "  JABALI_MINIMAL       Install only core components (set to any value)"
    echo "  MAIL_BACKEND         Mail backend: 'stalwart' (only supported backend)"
    echo ""
    echo "Installation Modes:"
    echo "  Full Installation    - Web, Mail, DNS"
    echo "  Minimal Installation - Web server only (Nginx, PHP, MariaDB, Redis)"
    echo "  Custom Installation  - Choose individual components interactively"
    echo ""
    echo "Examples:"
    echo ""
    echo "  Interactive install (prompts for options):"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash"
    echo ""
    echo "  Full install (non-interactive):"
    echo "    SERVER_HOSTNAME=panel.example.com JABALI_FULL=1 curl -fsSL ... | sudo bash"
    echo ""
    echo "  Minimal install (non-interactive):"
    echo "    SERVER_HOSTNAME=panel.example.com JABALI_MINIMAL=1 curl -fsSL ... | sudo bash"
    echo ""
    echo "  Install from custom git repository:"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash -s -- install --git http://git.example.com/org/repo.git"
    echo ""
    echo "  Install using self-hosted Gitea installer:"
    echo "    curl -fsSL http://gitea.example.com/org/jabali-panel/raw/branch/main/install.sh | sudo bash"
    echo ""
    echo "  Uninstall:"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash -s -- uninstall"
    echo ""
    echo "  Force uninstall (no prompts):"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash -s -- uninstall --force"
    echo ""
}

JABALI_SECURITY_REPO="https://raw.githubusercontent.com/shukiv/jabali-security/master/install.sh"
JABALI_ISOLATOR_REPO="https://github.com/shukiv/jabali-isolator.git"
JABALI_ISOLATOR_DIR="/usr/local/jabali-isolator"

configure_sshd() {
    header "Configuring SSH Access"

    # Create groups if they don't exist
    getent group sftpusers &>/dev/null || groupadd sftpusers
    getent group shellusers &>/dev/null || groupadd shellusers

    local sshd_config="/etc/ssh/sshd_config"

    # Add SFTP chroot block if missing
    if ! grep -q "Match Group sftpusers" "$sshd_config" 2>/dev/null; then
        cat >> "$sshd_config" <<'SSHD_SFTP'

# Jabali Panel — SFTP-only users (chroot to home directory)
Match Group sftpusers
    ChrootDirectory /home/%u
    ForceCommand internal-sftp
    AllowTcpForwarding no
    X11Forwarding no
SSHD_SFTP
        log "Added SFTP chroot block to sshd_config"
    fi

    # Add or update shell users block
    if ! grep -q "Match Group shellusers" "$sshd_config" 2>/dev/null; then
        cat >> "$sshd_config" <<'SSHD_SHELL'

# Jabali Panel — shell users (login shell handles isolation, no ForceCommand)
Match Group shellusers
    AllowTcpForwarding yes
    X11Forwarding no
SSHD_SHELL
        log "Added shell users block to sshd_config"
    else
        # Existing shellusers block — replace it with current config
        # Migration: removes ForceCommand (now using login shell via chsh)
        awk '
            /^Match Group shellusers/ { skip=1; next }
            /^Match / && skip { skip=0 }
            !skip { print }
        ' "$sshd_config" > "${sshd_config}.tmp" && mv "${sshd_config}.tmp" "$sshd_config"
        chmod 644 "$sshd_config"
        cat >> "$sshd_config" <<'SSHD_SHELL'

# Jabali Panel — shell users (login shell handles isolation, no ForceCommand)
Match Group shellusers
    AllowTcpForwarding yes
    X11Forwarding no
SSHD_SHELL
        log "Updated shellusers SSH config (removed ForceCommand, using login shell)"
    fi

    # Migrate existing shell users: set login shell, fix home ownership
    if getent group shellusers &>/dev/null; then
        local shell_members
        shell_members=$(getent group shellusers | cut -d: -f4)
        if [[ -n "$shell_members" ]]; then
            IFS=',' read -ra members <<< "$shell_members"
            for member in "${members[@]}"; do
                local member_home="/home/$member"
                # Set login shell to jabali-shell (migration from ForceCommand)
                local current_shell
                current_shell=$(getent passwd "$member" | cut -d: -f7)
                if [[ "$current_shell" != "/usr/local/bin/jabali-shell" ]]; then
                    usermod -s /usr/local/bin/jabali-shell "$member" 2>/dev/null || true
                    info "Set login shell for shell user: $member"
                fi
                # Fix home dir ownership
                if [[ -d "$member_home" ]] && [[ "$(stat -c '%U' "$member_home" 2>/dev/null)" == "root" ]]; then
                    chown "$member":"$member" "$member_home"
                    chmod 755 "$member_home"
                    info "Fixed home dir ownership for shell user: $member"
                fi
            done
        fi
    fi

    # Test and reload SSH
    if sshd -t 2>/dev/null; then
        systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
        log "SSH configured"
    else
        warn "sshd_config test failed — check manually"
    fi
}

install_jabali_shell() {
    header "Installing Jabali Shell Wrapper"

    # Install bubblewrap (fallback for LXC environments where nspawn isn't available)
    if ! command -v bwrap &>/dev/null; then
        info "Installing bubblewrap (bwrap) for sandbox fallback..."
        DEBIAN_FRONTEND=noninteractive apt-get install -y bubblewrap >/dev/null 2>&1 || \
            warn "Could not install bubblewrap — bwrap fallback will not be available"
    fi

    # Install main wrapper script (nspawn → bwrap → fail)
    local src="$JABALI_DIR/stubs/jabali-shell.sh"
    if [[ -f "$src" ]]; then
        cp "$src" /usr/local/bin/jabali-shell
        chmod 755 /usr/local/bin/jabali-shell
        chown root:root /usr/local/bin/jabali-shell
        log "Jabali shell wrapper installed"
    else
        warn "jabali-shell.sh stub not found — skipping"
    fi

    # Install bwrap wrapper script
    local bwrap_src="$JABALI_DIR/stubs/jabali-shell-bwrap.sh"
    if [[ -f "$bwrap_src" ]]; then
        cp "$bwrap_src" /usr/local/bin/jabali-shell-bwrap
        chmod 755 /usr/local/bin/jabali-shell-bwrap
        chown root:root /usr/local/bin/jabali-shell-bwrap
        log "Jabali bwrap shell wrapper installed"
    fi

    # Add jabali-shell to allowed shells
    if ! grep -q "/usr/local/bin/jabali-shell" /etc/shells 2>/dev/null; then
        echo "/usr/local/bin/jabali-shell" >> /etc/shells
    fi

    # Install polkit rule for container access
    local polkit_src="$JABALI_DIR/stubs/polkit-jabali-shell.rules"
    if [[ -f "$polkit_src" ]]; then
        mkdir -p /etc/polkit-1/rules.d
        cp "$polkit_src" /etc/polkit-1/rules.d/50-jabali-shell.rules
        chmod 644 /etc/polkit-1/rules.d/50-jabali-shell.rules
        log "Polkit rule installed for container shell access"
    fi

    # Install sudoers rule (nspawn + bwrap)
    local sudoers_file="/etc/sudoers.d/jabali-shell"
    cat > "$sudoers_file" <<'SUDOERS'
# Jabali Panel — allow shell users to enter their container via nsenter
%shellusers ALL=(root) NOPASSWD: /usr/bin/nsenter --target * --mount --pid --ipc --uts *
%shellusers ALL=(root) NOPASSWD: /usr/local/bin/jabali-isolate create *
%shellusers ALL=(root) NOPASSWD: /usr/local/bin/jabali-isolate start *
%shellusers ALL=(root) NOPASSWD: /usr/bin/tee /etc/php/*/fpm/pool.d/*.conf
SUDOERS
    chmod 440 "$sudoers_file"
    log "Sudoers rule installed for shell access"
}

install_jabali_security() {
    header "Installing Jabali Security"

    info "Downloading jabali-security installer..."
    if curl -fsSL --retry 3 --connect-timeout 30 "$JABALI_SECURITY_REPO" | JABALI_WEB=no bash; then
        log "Jabali Security installed"
    else
        warn "jabali-security installation failed (non-fatal)"
    fi

}

install_jabali_isolator() {
    header "Installing Jabali Isolator (PHP-FPM container isolation)"

    # Require systemd-container for systemd-nspawn
    if ! command -v systemd-nspawn &>/dev/null; then
        info "Installing systemd-container package..."
        DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-confold 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y systemd-container >/dev/null 2>&1 || {
            warn "Could not install systemd-container — jabali-isolator requires it"
            return
        }
    fi

    if [[ -d "$JABALI_ISOLATOR_DIR" ]]; then
        info "Updating existing jabali-isolator installation..."
        git -C "$JABALI_ISOLATOR_DIR" pull --ff-only 2>/dev/null || true
    else
        info "Cloning jabali-isolator..."
        if ! git clone "$JABALI_ISOLATOR_REPO" "$JABALI_ISOLATOR_DIR" 2>/dev/null; then
            warn "Could not clone jabali-isolator — skipping"
            return
        fi
    fi

    # Ensure uv is installed
    if ! command -v uv &>/dev/null; then
        info "Installing uv package manager..."
        curl -LsSf https://astral.sh/uv/install.sh | sh >/dev/null 2>&1 || {
            warn "Could not install uv — jabali-isolator requires it"
            return
        }
        export PATH="$HOME/.local/bin:$PATH"
    fi

    # Install into a venv with uv
    (cd "$JABALI_ISOLATOR_DIR" && uv sync 2>/dev/null) || {
        warn "uv sync failed for jabali-isolator"
        return
    }

    # Create CLI symlink
    local venv_bin="$JABALI_ISOLATOR_DIR/.venv/bin/jabali-isolate"
    if [[ -f "$venv_bin" ]]; then
        ln -sf "$venv_bin" /usr/local/bin/jabali-isolate
        info "Installed jabali-isolate CLI to /usr/local/bin/jabali-isolate"
    fi

    # Install idle container checker (stops containers with no active SSH sessions)
    install -m 755 "$JABALI_DIR/stubs/jabali-container-idle-check.sh" /usr/local/bin/jabali-container-idle-check

    cat > /etc/systemd/system/jabali-container-idle-check.service <<'IDLE_SVC'
[Unit]
Description=Jabali Container Idle Checker
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jabali-container-idle-check
IDLE_SVC

    cat > /etc/systemd/system/jabali-container-idle-check.timer <<'IDLE_TMR'
[Unit]
Description=Check for idle Jabali containers every 5 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
IDLE_TMR

    systemctl daemon-reload
    systemctl enable --now jabali-container-idle-check.timer 2>/dev/null || true

    info "Jabali Isolator installed"
}

# Main installation
main() {
    show_banner
    check_root
    check_os

    # Detect existing installation
    if [[ -d "/var/www/jabali" ]] && [[ -f "/var/www/jabali/.env" ]]; then
        local installed_version=""
        if [[ -f "/var/www/jabali/VERSION" ]]; then
            installed_version=$(sed -n 's/^VERSION=//p' /var/www/jabali/VERSION)
        fi

        echo ""
        echo -e "${YELLOW}${BOLD}Jabali Panel is already installed${installed_version:+ (v$installed_version)}.${NC}"
        echo ""
        echo "Options:"
        echo "  1) Re-install (uninstall + fresh install)"
        echo "  2) Cancel"
        echo ""
        read -p "Choose [1-2]: " reinstall_choice < /dev/tty

        case "$reinstall_choice" in
            1)
                # Back up .env and credentials before wiping
                local backup_dir="/root/.jabali_reinstall_backup_$(date +%Y%m%d_%H%M%S)"
                mkdir -p "$backup_dir"
                cp /var/www/jabali/.env "$backup_dir/.env" 2>/dev/null || true
                cp /root/.jabali_db_credentials "$backup_dir/db_credentials" 2>/dev/null || true
                cp /root/.jabali_redis_credentials "$backup_dir/redis_credentials" 2>/dev/null || true
                info "Backed up .env and credentials to $backup_dir"

                info "Uninstalling existing installation before re-install..."
                uninstall --force --keep-packages
                info "Previous installation removed. Starting fresh install..."
                echo ""
                ;;
            *)
                info "Installation cancelled"
                exit 0
                ;;
        esac
    fi

    local queue_was_active=false
    if systemctl is-active --quiet jabali-queue; then
        queue_was_active=true
        systemctl stop jabali-queue 2>/dev/null || true
    fi

    prompt_hostname
    select_features

    info "Starting installation, this may take several minutes..."
    echo ""

    add_repositories
    install_packages
    install_geoipupdate_binary
    install_composer
    install_frankenphp
    install_wp_cli
    clone_jabali
    configure_php
    configure_mariadb
    install_phpmyadmin
    configure_nginx

    # Optional components based on feature selection
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        configure_stalwart
    else
        info "Skipping Mail Server configuration"
    fi

    if [[ "$INSTALL_DNS" == "true" ]]; then
        configure_dns
    else
        info "Skipping DNS Server configuration"
    fi

    # Setup disk quotas for user space management
    setup_quotas
    configure_redis
    setup_jabali
    setup_agent_service
    setup_frankenphp_config
    setup_panel_service
    setup_queue_service
    setup_scheduler_cron
    setup_logrotate
    setup_panel_ssl
    setup_self_healing
    create_admin
    create_webmaster_mailbox
    configure_ssh_notifications

    if [[ "$queue_was_active" == "true" ]]; then
        systemctl start jabali-queue 2>/dev/null || true
    fi

    # Install jabali-security daemon
    install_jabali_security

    # Install jabali-isolator (PHP-FPM nspawn containers)
    install_jabali_isolator

    # Configure SSH access (SFTP chroot + nspawn shell)
    configure_sshd
    install_jabali_shell

    # Fix .git ownership so both root CLI and www-data panel can use it
    chown -R www-data:www-data "$JABALI_DIR/.git" 2>/dev/null || true

    print_completion
}

# Parse command line arguments
COMMAND="install"
UNINSTALL_FORCE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        install|reinstall|upgrade|uninstall|remove|purge|--help|-h|help)
            COMMAND="$1"
            shift
            ;;
        --git)
            if [[ -z "${2:-}" ]]; then
                error "Missing value for --git"
                show_usage
                exit 1
            fi
            JABALI_REPO="$2"
            shift 2
            ;;
        --branch)
            if [[ -z "${2:-}" ]]; then
                error "Missing value for --branch"
                show_usage
                exit 1
            fi
            JABALI_BRANCH="$2"
            shift 2
            ;;
        --force|-f)
            UNINSTALL_FORCE="--force"
            shift
            ;;
        --keep-packages|--keep)
            UNINSTALL_KEEP_PACKAGES="true"
            shift
            ;;
        --debug)
            DEBUG=true
            shift
            ;;
        *)
            error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

case "$COMMAND" in
    install)
        main
        ;;
    reinstall)
        reinstall
        ;;
    upgrade)
        upgrade_infra
        ;;
    uninstall|remove|purge)
        uninstall "$UNINSTALL_FORCE" ${UNINSTALL_KEEP_PACKAGES:+--keep-packages}
        ;;
    --help|-h|help)
        show_usage
        ;;
    *)
        error "Unknown command: $COMMAND"
        show_usage
        exit 1
        ;;
esac
