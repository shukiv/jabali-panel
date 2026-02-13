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

# Version - prefer local VERSION file if present, fallback for curl installs
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "$SCRIPT_DIR/VERSION" ]]; then
    JABALI_VERSION="$(sed -n 's/^VERSION=//p' "$SCRIPT_DIR/VERSION")"
fi
JABALI_VERSION="${JABALI_VERSION:-0.9-rc67}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Configuration
JABALI_DIR="/var/www/jabali"
JABALI_USER="www-data"
JABALI_REPO="https://github.com/shukiv/jabali-panel.git"
NODE_VERSION="20"

# PHP version will be detected after installation
PHP_VERSION=""

# Feature flags (default: all enabled)
INSTALL_MAIL=true
INSTALL_DNS=true
INSTALL_FIREWALL=true
INSTALL_SECURITY=true  # Fail2ban + ClamAV

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
        info "Minimal installation mode: Only core components will be installed"
        INSTALL_MAIL=false
        INSTALL_DNS=false
        INSTALL_SECURITY=false
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
    echo "     - Mail Server (Postfix, Dovecot, Rspamd)"
    echo "     - DNS Server (BIND9)"
    echo "     - Security (Firewall, Fail2ban, ClamAV)"
    echo ""
    echo "  2) Minimal Installation"
    echo "     - Web Server only (Nginx, PHP, MariaDB, Redis)"
    echo "     - Firewall (UFW)"
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
            INSTALL_DNS=false
            INSTALL_SECURITY=false
            ;;
        3)
            echo ""
            echo -e "${BOLD}Custom Installation${NC}"
            echo ""

            # Mail Server
            read -p "Install Mail Server (Postfix, Dovecot)? [Y/n]: " mail_choice < /dev/tty
            if [[ "$mail_choice" =~ ^[Nn]$ ]]; then
                INSTALL_MAIL=false
            fi

            # DNS Server
            read -p "Install DNS Server (BIND9)? [Y/n]: " dns_choice < /dev/tty
            if [[ "$dns_choice" =~ ^[Nn]$ ]]; then
                INSTALL_DNS=false
            fi

            # Firewall
            read -p "Install Firewall (UFW)? [Y/n]: " fw_choice < /dev/tty
            if [[ "$fw_choice" =~ ^[Nn]$ ]]; then
                INSTALL_FIREWALL=false
            fi

            # Security tools
            read -p "Install Security Tools (Fail2ban, ClamAV)? [Y/n]: " sec_choice < /dev/tty
            if [[ "$sec_choice" =~ ^[Nn]$ ]]; then
                INSTALL_SECURITY=false
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
    [[ "$INSTALL_MAIL" == "true" ]] && echo -e "  - Mail Server: ${GREEN}Yes${NC}" || echo -e "  - Mail Server: ${YELLOW}No${NC}"
    [[ "$INSTALL_DNS" == "true" ]] && echo -e "  - DNS Server: ${GREEN}Yes${NC}" || echo -e "  - DNS Server: ${YELLOW}No${NC}"
    [[ "$INSTALL_FIREWALL" == "true" ]] && echo -e "  - Firewall: ${GREEN}Yes${NC}" || echo -e "  - Firewall: ${YELLOW}No${NC}"
    [[ "$INSTALL_SECURITY" == "true" ]] && echo -e "  - Security Tools: ${GREEN}Yes${NC}" || echo -e "  - Security Tools: ${YELLOW}No${NC}"
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
        for ver in 8.4 8.3 8.2 8.1 8.0; do
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

            # Check for testing/unstable first (trixie/sid)
            if [[ "$debian_ver" == *trixie* ]] || [[ "$debian_ver" == *sid* ]] || \
               [[ "$VERSION_CODENAME" == "trixie" ]] || [[ "$VERSION_CODENAME" == "sid" ]] || \
               [[ -z "${VERSION_ID:-}" ]]; then
                info "Detected Debian testing/unstable (trixie/sid) - proceeding"
            elif [[ "$debian_ver" == *bookworm* ]] || [[ "$VERSION_CODENAME" == "bookworm" ]]; then
                info "Detected Debian 12 (bookworm)"
            elif [[ "$debian_ver" == *bullseye* ]] || [[ "$VERSION_CODENAME" == "bullseye" ]]; then
                info "Detected Debian 11 (bullseye)"
            elif [[ -n "${VERSION_ID:-}" ]] && [[ "${VERSION_ID}" -lt 11 ]]; then
                error "Debian 11 or later is required"
            else
                # Try numeric extraction from debian_version
                local num_ver=$(echo "$debian_ver" | grep -oE '^[0-9]+' | head -1)
                if [[ -n "$num_ver" ]] && [[ "$num_ver" -ge 11 ]]; then
                    info "Detected Debian $num_ver"
                else
                    warn "Unknown Debian version: $debian_ver - proceeding anyway"
                fi
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

    # Update package list
    apt-get update -qq

    # Install prerequisites (software-properties-common is optional, mainly for Ubuntu)
    apt-get install -y -qq apt-transport-https ca-certificates curl gnupg lsb-release sudo
    apt-get install -y -qq software-properties-common 2>/dev/null || true

    # Detect codename
    local codename=$(lsb_release -sc)

    # Ensure Debian contrib repository for geoipupdate and related packages
    if [[ -f /etc/debian_version ]]; then
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

    # Sury supports: bookworm, bullseye, buster, trixie (Debian) and jammy, focal, noble (Ubuntu)
    info "Using Sury PHP repository for $codename"

    # Add/update PHP repository (Sury) - always update to ensure correct codename
    info "Configuring PHP repository..."
    curl -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor --yes -o /usr/share/keyrings/sury-php.gpg 2>/dev/null || true
    echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${codename} main" > /etc/apt/sources.list.d/php.list

    # Add NodeJS repository
    if [[ ! -f /etc/apt/sources.list.d/nodesource.list ]]; then
        info "Adding NodeJS repository..."
        curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash - > /dev/null 2>&1
    fi

    # Add MariaDB repository (optional, system version usually fine)

    apt-get update -qq
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
    # Check if php8.4 packages exist in any state (installed, config-files, half-installed, etc.)
    # dpkg remembers "deleted" config files and won't recreate them on reinstall
    if dpkg -l 'php8.4*' 2>/dev/null | grep -qE '^(ii|rc|iU|iF|hi|pi)'; then
        # Check if configs are missing or broken
        if [[ ! -f /etc/php/8.4/fpm/php.ini ]] || [[ ! -f /etc/php/8.4/cli/php.ini ]] || \
           dpkg -l php8.4-fpm 2>/dev/null | grep -qE '^(rc|iU|iF)'; then
            info "Cleaning up broken PHP installation..."
            systemctl stop php8.4-fpm 2>/dev/null || true
            systemctl reset-failed php8.4-fpm 2>/dev/null || true
            DEBIAN_FRONTEND=noninteractive apt-get purge -y 'php8.4*' 2>/dev/null || true
            rm -rf /etc/php/8.4
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
        locales

        # Security (always installed)
        fail2ban
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

        # System metrics
        sysstat
    )

    # Add Mail Server packages if enabled
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        info "Including Mail Server packages..."
        base_packages+=(
            postfix
            postfix-mysql
            dovecot-core
            dovecot-imapd
            dovecot-pop3d
            dovecot-lmtpd
            dovecot-mysql
            dovecot-sqlite
            opendkim
            opendkim-tools
            rspamd
            # Webmail
            roundcube
            roundcube-core
            roundcube-sqlite3
            roundcube-plugins
            # SQLite tools
            sqlite3
        )
    fi

    # Add DNS Server packages if enabled
    if [[ "$INSTALL_DNS" == "true" ]]; then
        info "Including DNS Server packages..."
        base_packages+=(
            bind9
            bind9-utils
        )
    fi

    # Add Firewall packages if enabled
    if [[ "$INSTALL_FIREWALL" == "true" ]]; then
        info "Including Firewall packages..."
        base_packages+=(
            ufw
        )
    fi

    # Add Security packages if enabled
    if [[ "$INSTALL_SECURITY" == "true" ]]; then
        info "Including Security packages..."
        if apt-cache show libnginx-mod-http-modsecurity &>/dev/null; then
            base_packages+=(
                libnginx-mod-http-modsecurity
            )
        elif apt-cache show libnginx-mod-http-modsecurity2 &>/dev/null; then
            base_packages+=(
                libnginx-mod-http-modsecurity2
            )
        elif apt-cache show nginx-extras &>/dev/null; then
            base_packages+=(
                nginx-extras
            )
        else
            warn "ModSecurity nginx module not available in apt repositories"
        fi

        if apt-cache show libmodsecurity3t64 &>/dev/null; then
            base_packages+=(
                libmodsecurity3t64
            )
        elif apt-cache show libmodsecurity3 &>/dev/null; then
            base_packages+=(
                libmodsecurity3
            )
        fi

        if apt-cache show modsecurity-crs &>/dev/null; then
            base_packages+=(
                modsecurity-crs
            )
        fi

        base_packages+=(
            clamav
            clamav-daemon
            clamav-freshclam
            # Vulnerability scanners
            lynis
            # nikto is installed from GitHub (not in apt repos)
            # Ruby for WPScan
            ruby
            ruby-dev
        )
    fi

    # Prevent Apache2 and libapache2-mod-php from being installed
    # (roundcube recommends apache2, php metapackage recommends libapache2-mod-php, but we use nginx+php-fpm)
    info "Blocking Apache2 and mod-php installation (we use nginx + php-fpm)..."
    apt-mark hold apache2 libapache2-mod-php libapache2-mod-php8.4 2>/dev/null || true

    # Pre-configure postfix and roundcube to avoid interactive prompts
    # Note: debconf templates may not exist yet on fresh install, so suppress errors
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        echo "postfix postfix/mailname string $(hostname -f)" | debconf-set-selections 2>/dev/null || true
        echo "postfix postfix/main_mailer_type string 'Internet Site'" | debconf-set-selections 2>/dev/null || true
        # Skip roundcube dbconfig - we configure it manually
        echo "roundcube-core roundcube/dbconfig-install boolean false" | debconf-set-selections 2>/dev/null || true
        echo "roundcube-core roundcube/database-type select sqlite3" | debconf-set-selections 2>/dev/null || true
    fi

    info "Installing base packages..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${base_packages[@]}" || {
        warn "Some packages may not be available, retrying individually..."
        for pkg in "${base_packages[@]}"; do
            apt-get install -y -qq "$pkg" 2>/dev/null || warn "Could not install: $pkg"
        done
    }

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
    apt-mark unhold apache2 libapache2-mod-php libapache2-mod-php8.4 2>/dev/null || true

    # Install PHP 8.4 (required for Jabali Panel)
    info "Installing PHP 8.4..."

    # Stop, disable and mask Apache2 if installed (conflicts with nginx on port 80)
    # Apache2 can be installed as a dependency of some PHP packages
    if dpkg -l apache2 2>/dev/null | grep -q '^ii' || systemctl is-active --quiet apache2 2>/dev/null; then
        info "Stopping Apache2 (conflicts with nginx)..."
        systemctl stop apache2 2>/dev/null || true
        systemctl disable apache2 2>/dev/null || true
        systemctl mask apache2 2>/dev/null || true
    fi

    # Clean up any broken PHP-FPM state from previous installations
    if systemctl is-failed --quiet php8.4-fpm 2>/dev/null; then
        info "Resetting failed PHP-FPM service state..."
        systemctl reset-failed php8.4-fpm
    fi

    # Required PHP extensions for Jabali Panel
    local php_extensions=(
        php8.4
        php8.4-fpm
        php8.4-cli
        php8.4-common
        php8.4-mysql
        php8.4-pgsql
        php8.4-sqlite3
        php8.4-curl
        php8.4-gd
        php8.4-mbstring
        php8.4-xml          # Provides dom extension
        php8.4-zip
        php8.4-bcmath
        php8.4-intl         # Required by Filament
        php8.4-readline
        php8.4-soap
        php8.4-imap
        php8.4-ldap
        php8.4-imagick
        php8.4-redis
        php8.4-opcache
    )

    # Install all PHP packages (use --force-confmiss to handle dpkg's "deleted config" state)
    if ! DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" "${php_extensions[@]}"; then
        warn "PHP installation had errors, attempting aggressive recovery..."

        # Stop PHP-FPM if it's somehow running in a broken state
        systemctl stop php8.4-fpm 2>/dev/null || true
        systemctl reset-failed php8.4-fpm 2>/dev/null || true

        # Purge ALL PHP 8.4 packages including config files
        info "Purging all PHP 8.4 packages..."
        DEBIAN_FRONTEND=noninteractive apt-get purge -y 'php8.4*' 2>/dev/null || true

        # Also remove libapache2-mod-php if it got installed (it conflicts with php-fpm)
        DEBIAN_FRONTEND=noninteractive apt-get purge -y 'libapache2-mod-php*' 2>/dev/null || true

        # Remove config directories to force fresh install (dpkg won't replace "deleted" configs)
        info "Removing PHP config directories..."
        rm -rf /etc/php/8.4/fpm
        rm -rf /etc/php/8.4/cli
        rm -rf /etc/php/8.4/apache2

        # Clean package cache
        apt-get clean
        apt-get autoclean

        # Fix any broken dpkg state
        dpkg --configure -a 2>/dev/null || true

        # Reinstall PHP with force-confmiss to ensure config files are created
        info "Reinstalling PHP 8.4 with fresh configuration..."
        if ! DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" "${php_extensions[@]}"; then
            error "Failed to install PHP 8.4. Please check your system's package state and try again."
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

    # Verify PHP 8.4 is installed and working
    if ! php -v 2>/dev/null | grep -q "PHP 8.4"; then
        error "PHP 8.4 installation failed. Found: $(php -v 2>/dev/null | head -1)"
    fi

    PHP_VERSION="8.4"
    log "PHP 8.4 installed successfully"

    # Ensure PHP-FPM is properly configured
    if [[ ! -f "/etc/php/8.4/fpm/php-fpm.conf" ]] || [[ ! -f "/etc/php/8.4/fpm/php.ini" ]]; then
        warn "PHP-FPM config files missing after install"
        info "Purging and reinstalling PHP-FPM with fresh config..."
        systemctl stop php8.4-fpm 2>/dev/null || true
        systemctl reset-failed php8.4-fpm 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php8.4-fpm 2>/dev/null || true
        rm -rf /etc/php/8.4/fpm
        apt-get clean
        DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" php8.4-fpm
    fi

    # Verify PHP-FPM is running
    if ! systemctl is-active --quiet php8.4-fpm; then
        # Reset failed state first if needed
        systemctl reset-failed php8.4-fpm 2>/dev/null || true
        if ! systemctl start php8.4-fpm; then
            warn "PHP-FPM failed to start, attempting recovery..."
            # Check for config errors
            php-fpm8.4 -t 2>&1 || true
            systemctl status php8.4-fpm --no-pager -l || true
        fi
    fi

    # Create dedicated PHP-FPM service for the panel to avoid interrupting user pools
    local panel_fpm_dir="/etc/php/8.4/fpm-panel"
    mkdir -p "${panel_fpm_dir}/pool.d"

    cat > "${panel_fpm_dir}/php-fpm.conf" <<'EOF'
[global]
pid = /run/php/php8.4-fpm-panel.pid
error_log = /var/log/php8.4-fpm-panel.log
include=/etc/php/8.4/fpm-panel/pool.d/*.conf
EOF

    cat > "${panel_fpm_dir}/pool.d/panel.conf" <<'EOF'
[panel]
user = www-data
group = www-data
listen = /run/php/php8.4-fpm-panel.sock
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
php_admin_value[error_log] = /var/log/php8.4-fpm-panel.pool.log
chdir = /
EOF

    cat > /etc/systemd/system/php8.4-fpm-panel.service <<'EOF'
[Unit]
Description=The PHP 8.4 FastCGI Process Manager (Panel)
Documentation=man:php-fpm8.4(8)
After=network.target

[Service]
Type=notify
ExecStart=/usr/sbin/php-fpm8.4 --nodaemonize --fpm-config /etc/php/8.4/fpm-panel/php-fpm.conf
ExecReload=/bin/kill -USR2 $MAINPID
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now php8.4-fpm-panel

    # Verify PHP CLI is working and has required extensions
    info "Verifying PHP CLI and extensions..."

    if ! command -v php &>/dev/null; then
        error "PHP CLI is not in PATH after installation."
    fi

    # Ensure php.ini exists for CLI (dpkg doesn't replace deleted config files)
    local cli_ini="/etc/php/8.4/cli/php.ini"

    if [[ ! -f "$cli_ini" ]]; then
        warn "PHP CLI config file missing: $cli_ini"
        info "Reinstalling php8.4-cli with fresh config..."
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php8.4-cli 2>/dev/null || true
        rm -rf /etc/php/8.4/cli
        DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confmiss" php8.4-cli
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
        DEBIAN_FRONTEND=noninteractive apt-get purge -y php8.4-common
        DEBIAN_FRONTEND=noninteractive apt-get install -y "${php_extensions[@]}"

        # Check again
        php -r "class_exists('Phar') || exit(1);" 2>/dev/null || error "PHP Phar extension is missing"
        php -r "extension_loaded('dom') || exit(1);" 2>/dev/null || error "PHP DOM extension is missing (install php8.4-xml)"
        php -r "extension_loaded('intl') || exit(1);" 2>/dev/null || error "PHP Intl extension is missing (install php8.4-intl)"
    fi

    log "PHP 8.4 CLI verified with all required extensions"

    # Install WPScan if security is enabled
    if [[ "$INSTALL_SECURITY" == "true" ]]; then
        info "Installing WPScan..."
        if command -v gem &> /dev/null; then
            gem install wpscan --no-document 2>/dev/null && {
                log "WPScan installed successfully"
            } || {
                warn "WPScan installation failed (may require more memory)"
            }
        else
            warn "Ruby gem not available, skipping WPScan"
        fi

        # Install Nikto from GitHub if not available via apt
        if ! command -v nikto &> /dev/null; then
            info "Installing Nikto from GitHub..."
            if [[ ! -d "/opt/nikto" ]]; then
                git clone https://github.com/sullo/nikto.git /opt/nikto 2>/dev/null && {
                    ln -sf /opt/nikto/program/nikto.pl /usr/local/bin/nikto
                    chmod +x /opt/nikto/program/nikto.pl
                    log "Nikto installed successfully"
                } || {
                    warn "Nikto installation failed"
                }
            fi
        fi
    fi

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
    curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer

    if ! command -v composer &>/dev/null; then
        error "Composer installation failed"
    fi

    log "Composer installed"
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

# Clone Jabali Panel
clone_jabali() {
    header "Installing Jabali Panel"

    if [[ -d "$JABALI_DIR" ]]; then
        warn "Jabali directory exists, backing up..."
        mv "$JABALI_DIR" "${JABALI_DIR}.bak.$(date +%s)"
    fi

    git clone "$JABALI_REPO" "$JABALI_DIR"
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
    systemctl start php${PHP_VERSION}-fpm 2>/dev/null || true

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

configure_sysstat() {
    if ! command -v sar >/dev/null 2>&1; then
        warn "sysstat not installed, skipping sysstat configuration"
        return
    fi

    info "Configuring sysstat..."

    if [[ -f /etc/default/sysstat ]]; then
        if grep -q '^ENABLED=' /etc/default/sysstat; then
            sed -i 's/^ENABLED=.*/ENABLED=\"true\"/' /etc/default/sysstat
        else
            echo 'ENABLED="true"' >> /etc/default/sysstat
        fi

        if grep -q '^INTERVAL=' /etc/default/sysstat; then
            sed -i 's/^INTERVAL=.*/INTERVAL=10/' /etc/default/sysstat
        else
            echo 'INTERVAL=10' >> /etc/default/sysstat
        fi
    fi

    if [[ -f /etc/sysstat/sysstat ]]; then
        if grep -q '^HISTORY=' /etc/sysstat/sysstat; then
            sed -i 's/^HISTORY=.*/HISTORY=31/' /etc/sysstat/sysstat
        else
            echo 'HISTORY=31' >> /etc/sysstat/sysstat
        fi

        if grep -q '^REPORTS=' /etc/sysstat/sysstat; then
            sed -i 's/^REPORTS=.*/REPORTS=true/' /etc/sysstat/sysstat
        else
            echo 'REPORTS=true' >> /etc/sysstat/sysstat
        fi

        if grep -q '^SADC_OPTIONS=' /etc/sysstat/sysstat; then
            sed -i 's/^SADC_OPTIONS=.*/SADC_OPTIONS=\"-S DISK\"/' /etc/sysstat/sysstat
        else
            echo 'SADC_OPTIONS="-S DISK"' >> /etc/sysstat/sysstat
        fi

        if grep -q '^ENABLED=' /etc/sysstat/sysstat; then
            sed -i 's/^ENABLED=.*/ENABLED=\"true\"/' /etc/sysstat/sysstat
        else
            echo 'ENABLED="true"' >> /etc/sysstat/sysstat
        fi

        if grep -q '^INTERVAL=' /etc/sysstat/sysstat; then
            sed -i 's/^INTERVAL=.*/INTERVAL=10/' /etc/sysstat/sysstat
        else
            echo 'INTERVAL=10' >> /etc/sysstat/sysstat
        fi
    fi

    if systemctl list-unit-files | grep -q '^sysstat-collect.timer'; then
        mkdir -p /etc/systemd/system/sysstat-collect.timer.d
        cat > /etc/systemd/system/sysstat-collect.timer.d/override.conf <<'EOF'
[Timer]
OnCalendar=
OnActiveSec=10s
OnUnitActiveSec=10s
AccuracySec=1s
Persistent=true
EOF
    fi

    mkdir -p /var/log/sysstat
    chmod 0755 /var/log/sysstat

    systemctl daemon-reload 2>/dev/null || true
    systemctl enable --now sysstat-collect.timer 2>/dev/null || true
    systemctl enable --now sysstat-summary.timer 2>/dev/null || true
    if systemctl list-unit-files | grep -q '^sysstat.service'; then
        systemctl enable --now sysstat.service 2>/dev/null || true
    fi
    systemctl restart sysstat-collect.timer 2>/dev/null || true
    systemctl restart sysstat-summary.timer 2>/dev/null || true

    if [[ -x /usr/libexec/sysstat/sa1 ]]; then
        /usr/libexec/sysstat/sa1 1 1 >/dev/null 2>&1 || true
    elif [[ -x /usr/lib/sysstat/sa1 ]]; then
        /usr/lib/sysstat/sa1 1 1 >/dev/null 2>&1 || true
    elif [[ -x /usr/lib/sysstat/sadc ]]; then
        /usr/lib/sysstat/sadc 1 1 /var/log/sysstat/sa"$(date +%d)" >/dev/null 2>&1 || true
    elif command -v sadc >/dev/null 2>&1; then
        sadc 1 1 /var/log/sysstat/sa"$(date +%d)" >/dev/null 2>&1 || true
    fi
}

# Configure MariaDB
configure_mariadb() {
    header "Configuring MariaDB"

    systemctl enable mariadb
    systemctl start mariadb

    # Secure installation (non-interactive)
    mysql -e "DELETE FROM mysql.user WHERE User='';"
    mysql -e "DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost', '127.0.0.1', '::1');"
    mysql -e "DROP DATABASE IF EXISTS test;"
    mysql -e "DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';"
    mysql -e "FLUSH PRIVILEGES;"

    # Create Jabali database
    local db_password=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)
    mysql -e "CREATE DATABASE IF NOT EXISTS jabali CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
    mysql -e "CREATE USER IF NOT EXISTS 'jabali'@'localhost' IDENTIFIED BY '${db_password}';"
    mysql -e "GRANT ALL PRIVILEGES ON jabali.* TO 'jabali'@'localhost';"
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

    # Remove default site
    rm -f /etc/nginx/sites-enabled/default

    # Configure nginx global settings for large uploads
    local nginx_conf="/etc/nginx/nginx.conf"
    if [[ -f "$nginx_conf" ]]; then
        # Add timeouts if not present
        if ! grep -q "client_body_timeout" "$nginx_conf"; then
            sed -i '/server_tokens/a\    client_max_body_size 512M;\n    client_body_timeout 600s;\n    fastcgi_read_timeout 600s;\n    proxy_read_timeout 600s;' "$nginx_conf"
        fi

        # Add FastCGI cache settings if not present
        if ! grep -q "fastcgi_cache_path" "$nginx_conf"; then
            # Create cache directory
            mkdir -p /var/cache/nginx/fastcgi
            chown www-data:www-data /var/cache/nginx/fastcgi

            # Add FastCGI cache configuration before the closing brace of http block
            sed -i '/^http {/,/^}/ {
                /include \/etc\/nginx\/sites-enabled\/\*;/a\
\
\t##\
\t# FastCGI Cache\
\t##\
\
\tfastcgi_cache_path /var/cache/nginx/fastcgi levels=1:2 keys_zone=JABALI:100m inactive=60m max_size=1g;\
\tfastcgi_cache_key "$scheme$request_method$host$request_uri";\
\tfastcgi_cache_use_stale error timeout invalid_header http_500 http_503;
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

    local panel_sock="/run/php/php${PHP_VERSION}-fpm-panel.sock"

    # Generate self-signed SSL certificate for the panel
    log "Generating self-signed SSL certificate..."
    local ssl_dir="/etc/ssl/jabali"
    mkdir -p "$ssl_dir"

    # Generate private key and self-signed certificate (valid for 10 years)
    openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
        -keyout "$ssl_dir/panel.key" \
        -out "$ssl_dir/panel.crt" \
        -subj "/C=US/ST=State/L=City/O=Jabali Panel/CN=${SERVER_HOSTNAME:-localhost}" \
        2>/dev/null

    chmod 600 "$ssl_dir/panel.key"
    chmod 644 "$ssl_dir/panel.crt"

    # Ensure Jabali Nginx include files exist for WAF/Geo includes
    local jabali_includes="/etc/nginx/jabali/includes"
    mkdir -p "$jabali_includes"
    if [[ ! -f "$jabali_includes/waf.conf" ]]; then
        cat > "$jabali_includes/waf.conf" <<'EOF'
# Managed by Jabali
modsecurity off;
EOF
    fi
    if [[ ! -f "$jabali_includes/geo.conf" ]]; then
        echo "# Managed by Jabali" > "$jabali_includes/geo.conf"
    fi

    # Create Jabali site config with HTTPS and HTTP redirect
    cat > /etc/nginx/sites-available/${SERVER_HOSTNAME} << NGINX
# Redirect HTTP to HTTPS
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name ${SERVER_HOSTNAME} _;
    return 301 https://\$host\$request_uri;
}

# HTTPS server
server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    http2 on;
    server_name ${SERVER_HOSTNAME} _;
    root /var/www/jabali/public;
    index index.php index.html;

    # SSL Configuration
    ssl_certificate /etc/ssl/jabali/panel.crt;
    ssl_certificate_key /etc/ssl/jabali/panel.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;

    # Security headers
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    client_max_body_size 512M;

    location / {
        try_files \$uri \$uri/ /index.php?\$query_string;
    }

    location ~ \.php\$ {
        fastcgi_pass unix:${panel_sock};
        fastcgi_param SCRIPT_FILENAME \$realpath_root\$fastcgi_script_name;
        include fastcgi_params;
        fastcgi_read_timeout 600;
    }

    location ~ /\.(?!well-known).* {
        deny all;
    }

    # Roundcube Webmail
    location = /webmail {
        return 301 /webmail/;
    }

    location ^~ /webmail/ {
        alias /var/lib/roundcube/public_html/;
        index index.php;

        location ~ \.php\$ {
            fastcgi_pass unix:${panel_sock};
            fastcgi_param SCRIPT_FILENAME \$request_filename;
            include fastcgi_params;
            fastcgi_read_timeout 600;
        }
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
                    if [[ "$logdir" =~ ^/home/([^/]+)/domains/([^/]+)/logs$ ]]; then
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

    nginx -t && systemctl reload nginx

    log "Nginx configured with HTTPS (self-signed certificate)"
}

# Configure Mail Server
configure_mail() {
    header "Configuring Mail Server"

    # Basic Postfix config
    postconf -e "smtpd_tls_cert_file=/etc/ssl/certs/ssl-cert-snakeoil.pem"
    postconf -e "smtpd_tls_key_file=/etc/ssl/private/ssl-cert-snakeoil.key"
    postconf -e "smtpd_tls_security_level=may"
    postconf -e "smtpd_tls_auth_only=yes"
    postconf -e "virtual_transport=lmtp:unix:private/dovecot-lmtp"
    postconf -e "virtual_mailbox_domains=hash:/etc/postfix/virtual_mailbox_domains"
    postconf -e "virtual_mailbox_maps=hash:/etc/postfix/virtual_mailbox_maps"
    postconf -e "virtual_alias_maps=hash:/etc/postfix/virtual_aliases"

    # Create empty virtual map files and generate hash databases
    touch /etc/postfix/virtual_mailbox_domains
    touch /etc/postfix/virtual_mailbox_maps
    touch /etc/postfix/virtual_aliases
    postmap /etc/postfix/virtual_mailbox_domains
    postmap /etc/postfix/virtual_mailbox_maps
    postmap /etc/postfix/virtual_aliases

    # Configure submission port (587) for authenticated mail clients
    if ! grep -q "^submission" /etc/postfix/master.cf; then
        cat >> /etc/postfix/master.cf << 'SUBMISSION'

# Submission port for authenticated mail clients
submission inet n       -       y       -       -       smtpd
  -o syslog_name=postfix/submission
  -o smtpd_tls_security_level=encrypt
  -o smtpd_sasl_auth_enable=yes
  -o smtpd_sasl_type=dovecot
  -o smtpd_sasl_path=private/auth
  -o smtpd_client_restrictions=permit_sasl_authenticated,reject
  -o milter_macro_daemon_name=ORIGINATING
SUBMISSION
    fi

    # Basic Dovecot config
    mkdir -p /var/mail/vhosts
    groupadd -g 5000 vmail 2>/dev/null || true
    useradd -g vmail -u 5000 vmail -d /var/mail 2>/dev/null || true
    chown -R vmail:vmail /var/mail

    # Add dovecot to www-data group for SQLite access
    usermod -a -G www-data dovecot 2>/dev/null || true

    # Configure Dovecot SQL authentication for SQLite (Dovecot 2.4 format)
    info "Configuring Dovecot SQL authentication..."
    cat > /etc/dovecot/conf.d/auth-sql.conf.ext << 'DOVECOT_SQL'
# Authentication for SQL users - Jabali Panel
# Dovecot 2.4 configuration format

sql_driver = sqlite
sqlite_path = /var/www/jabali/database/database.sqlite

passdb sql {
  query = SELECT \
    m.local_part || '@' || d.domain AS user, \
    m.password_hash AS password \
  FROM mailboxes m \
  JOIN email_domains e ON m.email_domain_id = e.id \
  JOIN domains d ON e.domain_id = d.id \
  WHERE m.local_part || '@' || d.domain = '%{user}' \
    AND m.is_active = 1 \
    AND e.is_active = 1
}

userdb sql {
  query = SELECT \
    m.maildir_path AS home, \
    m.system_uid AS uid, \
    m.system_gid AS gid \
  FROM mailboxes m \
  JOIN email_domains e ON m.email_domain_id = e.id \
  JOIN domains d ON e.domain_id = d.id \
  WHERE m.local_part || '@' || d.domain = '%{user}' \
    AND m.is_active = 1

  iterate_query = SELECT m.local_part || '@' || d.domain AS user \
    FROM mailboxes m \
    JOIN email_domains e ON m.email_domain_id = e.id \
    JOIN domains d ON e.domain_id = d.id \
    WHERE m.is_active = 1
}
DOVECOT_SQL

    # Configure Dovecot master user for Jabali SSO
    if [[ ! -d /etc/jabali ]]; then
        mkdir -p /etc/jabali
        chown root:www-data /etc/jabali
        chmod 750 /etc/jabali
    fi

    master_user="jabali-master"
    master_pass=""

    if [[ -f /etc/jabali/roundcube-sso.conf ]]; then
        master_user=$(grep -m1 '^JABALI_SSO_MASTER_USER=' /etc/jabali/roundcube-sso.conf | cut -d= -f2-)
        master_pass=$(grep -m1 '^JABALI_SSO_MASTER_PASS=' /etc/jabali/roundcube-sso.conf | cut -d= -f2-)
    fi

    if [[ -z "$master_user" ]]; then
        master_user="jabali-master"
    fi

    if [[ -z "$master_pass" ]]; then
        master_pass=$(openssl rand -hex 24)
        cat > /etc/jabali/roundcube-sso.conf <<EOF
JABALI_SSO_MASTER_USER=${master_user}
JABALI_SSO_MASTER_PASS=${master_pass}
EOF
        chown root:www-data /etc/jabali/roundcube-sso.conf
        chmod 640 /etc/jabali/roundcube-sso.conf
    fi

    if command -v doveadm >/dev/null 2>&1; then
        master_hash=$(doveadm pw -s SHA512-CRYPT -p "$master_pass")
    else
        master_hash="{SHA512-CRYPT}$(openssl passwd -6 "$master_pass")"
    fi

    cat > /etc/dovecot/master-users <<EOF
${master_user}:${master_hash}
EOF
    chown root:dovecot /etc/dovecot/master-users
    chmod 640 /etc/dovecot/master-users

    cat > /etc/dovecot/conf.d/auth-master.conf.ext << 'DOVECOT_MASTER'
passdb {
  driver = passwd-file
  args = /etc/dovecot/master-users
  master = yes
}
DOVECOT_MASTER

    # Enable SQL auth in Dovecot (disable system auth, enable SQL auth)
    if [[ -f /etc/dovecot/conf.d/10-auth.conf ]]; then
        # Comment out system auth if not already
        sed -i 's/^!include auth-system.conf.ext/#!include auth-system.conf.ext/' /etc/dovecot/conf.d/10-auth.conf
        # Enable SQL auth if not already
        if ! grep -q "^!include auth-sql.conf.ext" /etc/dovecot/conf.d/10-auth.conf; then
            sed -i 's/#!include auth-sql.conf.ext/!include auth-sql.conf.ext/' /etc/dovecot/conf.d/10-auth.conf
            # If the line doesn't exist at all, add it
            if ! grep -q "auth-sql.conf.ext" /etc/dovecot/conf.d/10-auth.conf; then
                echo "!include auth-sql.conf.ext" >> /etc/dovecot/conf.d/10-auth.conf
            fi
        fi
        if ! grep -q "^auth_master_user_separator" /etc/dovecot/conf.d/10-auth.conf; then
            echo "auth_master_user_separator = *" >> /etc/dovecot/conf.d/10-auth.conf
        fi
        if ! grep -q "auth-master.conf.ext" /etc/dovecot/conf.d/10-auth.conf; then
            echo "!include auth-master.conf.ext" >> /etc/dovecot/conf.d/10-auth.conf
        fi
    fi

    # Configure Dovecot sockets for Postfix
    if [[ -f /etc/dovecot/conf.d/10-master.conf ]]; then
        # Add Postfix auth socket for SASL authentication
        # Note: Check for our specific comment, not just the socket path (default config has commented example)
        if ! grep -q "Postfix SMTP authentication socket" /etc/dovecot/conf.d/10-master.conf; then
            cat >> /etc/dovecot/conf.d/10-master.conf << 'DOVECOT_AUTH'

# Postfix SMTP authentication socket
service auth {
  unix_listener /var/spool/postfix/private/auth {
    mode = 0660
    user = postfix
    group = postfix
  }
}
DOVECOT_AUTH
        fi

        # Add LMTP socket for Postfix mail delivery
        # Note: Check for our specific comment
        if ! grep -q "LMTP socket for Postfix mail delivery" /etc/dovecot/conf.d/10-master.conf; then
            cat >> /etc/dovecot/conf.d/10-master.conf << 'DOVECOT_LMTP'

# LMTP socket for Postfix mail delivery
service lmtp {
  unix_listener /var/spool/postfix/private/dovecot-lmtp {
    mode = 0600
    user = postfix
    group = postfix
  }
}
DOVECOT_LMTP
        fi
    fi

    # Fix LMTP auth_username_format to keep full email address
    if [[ -f /etc/dovecot/conf.d/20-lmtp.conf ]]; then
        sed -i 's/auth_username_format = %{user | username | lower}/#auth_username_format = %{user | username | lower}/' /etc/dovecot/conf.d/20-lmtp.conf
    fi

    # Configure Dovecot mail storage (Maildir format)
    cat > /etc/dovecot/conf.d/10-mail.conf << 'DOVECOT_MAIL'
##
## Mailbox locations and namespaces - Jabali Panel
##

# Mail storage format and location
# Using Maildir format - home is returned by userdb
mail_driver = maildir
mail_home = %{userdb:home}
mail_path = %{userdb:home}

namespace inbox {
  inbox = yes
  separator = /
}
DOVECOT_MAIL

    systemctl enable postfix dovecot

    # Create Roundcube SSO script for Jabali Panel
    if [[ -d /var/lib/roundcube/public_html ]]; then
        cat > /var/lib/roundcube/public_html/jabali-sso.php << 'RCUBE_SSO'
<?php
/**
 * Jabali Panel SSO login for Roundcube
 * Uses direct login API instead of form submission
 */

$token = $_GET["token"] ?? "";

if (empty($token) || !preg_match("/^[a-f0-9]{64}$/", $token)) {
    die("Invalid token");
}

$cacheFile = "/tmp/roundcube_sso_" . $token;
if (!file_exists($cacheFile)) {
    die("Token expired or invalid");
}

$data = json_decode(file_get_contents($cacheFile), true);
if (!$data) {
    @unlink($cacheFile);
    die("Invalid token data");
}

@unlink($cacheFile);

if (isset($data["expires"]) && time() > $data["expires"]) {
    die("Token expired");
}

$masterUser = "";
$masterPass = "";
$masterConfig = "/etc/jabali/roundcube-sso.conf";
if (is_readable($masterConfig)) {
    $lines = file($masterConfig, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
    foreach ($lines as $line) {
        if (!str_contains($line, "=")) {
            continue;
        }
        [$key, $value] = explode("=", $line, 2);
        $key = trim($key);
        $value = trim($value);
        if ($key === "JABALI_SSO_MASTER_USER") {
            $masterUser = $value;
        } elseif ($key === "JABALI_SSO_MASTER_PASS") {
            $masterPass = $value;
        }
    }
}

$useMaster = !empty($data["use_master"]);
if ($useMaster) {
    $loginAs = trim($data["login_as"] ?? "");
    if ($loginAs === "" || $masterUser === "" || $masterPass === "") {
        die("SSO master login is not configured");
    }
    $authUser = $loginAs . "*" . $masterUser;
    $authPass = $masterPass;
} else {
    if (!isset($data["email"]) || !isset($data["password"])) {
        die("Invalid token data");
    }
    $authUser = trim($data["email"]);
    $authPass = $data["password"];
}

// Initialize Roundcube
define("INSTALL_PATH", "/var/lib/roundcube/");
require_once "/usr/share/roundcube/program/include/iniset.php";

$rcmail = rcmail::get_instance(0, "web");

// Perform direct login instead of form submission
$auth = $rcmail->plugins->exec_hook("authenticate", [
    "host" => $rcmail->autoselect_host(),
    "user" => $authUser,
    "pass" => $authPass,
    "valid" => true,
    "cookiecheck" => false,
]);

if ($auth["valid"] && !$auth["abort"]) {
    $login = $rcmail->login($auth["user"], $auth["pass"], $auth["host"], $auth["cookiecheck"]);

    if ($login) {
        // Login successful - redirect to inbox
        $rcmail->session->regenerate_id(false);
        $rcmail->session->set_auth_cookie();
        header("Location: /webmail/?_task=mail");
        exit;
    }
}

// Login failed - show error
?>
<!DOCTYPE html>
<html>
<head><title>Login Failed</title></head>
<body>
<p>Login failed. Please try again or contact support.</p>
<p><a href="/webmail/">Go to webmail login</a></p>
</body>
</html>
RCUBE_SSO
        chmod 755 /var/lib/roundcube/public_html/jabali-sso.php
        log "Roundcube SSO script installed"
    fi

    # Configure Roundcube SMTP with TLS (use 127.0.0.1 instead of localhost for reliable TLS)
    if [[ -f /etc/roundcube/config.inc.php ]]; then
        sed -i "s|\$config\['smtp_host'\] = 'localhost:587';|\$config['smtp_host'] = 'tls://127.0.0.1:587';|g" /etc/roundcube/config.inc.php
        sed -i "s|\$config\['smtp_host'\] = 'tls://localhost:587';|\$config['smtp_host'] = 'tls://127.0.0.1:587';|g" /etc/roundcube/config.inc.php
        # Add SMTP SSL options if not present (disable cert verification for localhost)
        if ! grep -q "smtp_conn_options" /etc/roundcube/config.inc.php; then
            cat >> /etc/roundcube/config.inc.php << 'SMTP_SSL'

// Disable TLS certificate verification for localhost SMTP
$config['smtp_conn_options'] = [
    'ssl' => [
        'verify_peer' => false,
        'verify_peer_name' => false,
        'allow_self_signed' => true,
    ],
];
SMTP_SSL
        fi
    fi
    if [[ -f /var/lib/roundcube/config/config.inc.php ]]; then
        sed -i "s|\$config\['smtp_host'\] = 'localhost:587';|\$config['smtp_host'] = 'tls://127.0.0.1:587';|g" /var/lib/roundcube/config/config.inc.php
        sed -i "s|\$config\['smtp_host'\] = 'tls://localhost:587';|\$config['smtp_host'] = 'tls://127.0.0.1:587';|g" /var/lib/roundcube/config/config.inc.php
    fi

    # Fix Roundcube config file permissions so www-data can read them
    # This is required for the SSO script to work
    chown root:www-data /etc/roundcube/config.inc.php /etc/roundcube/debian-db.php 2>/dev/null
    chmod 640 /etc/roundcube/config.inc.php /etc/roundcube/debian-db.php 2>/dev/null
    chown root:www-data /var/lib/roundcube/config/config.inc.php /var/lib/roundcube/config/debian-db.php 2>/dev/null
    chmod 640 /var/lib/roundcube/config/config.inc.php /var/lib/roundcube/config/debian-db.php 2>/dev/null

    # Fix Roundcube SQLite database configuration
    # The default debian-db.php has empty $basepath which causes DB errors
    if [[ -f /etc/roundcube/debian-db.php ]]; then
        cat > /etc/roundcube/debian-db.php << 'RCUBE_DB'
<?php
// Roundcube SQLite database configuration
// Configured by Jabali installer
$dbuser='roundcube';
$dbpass='';
$basepath='/var/lib/roundcube';
$dbname='roundcube.db';
$dbserver='';
$dbport='';
$dbtype='sqlite3';
RCUBE_DB
        chown root:www-data /etc/roundcube/debian-db.php
        chmod 640 /etc/roundcube/debian-db.php

        # Initialize SQLite database if it doesn't exist
        if [[ ! -f /var/lib/roundcube/roundcube.db ]]; then
            mkdir -p /var/lib/roundcube
            if [[ -f /usr/share/roundcube/SQL/sqlite.initial.sql ]]; then
                sqlite3 /var/lib/roundcube/roundcube.db < /usr/share/roundcube/SQL/sqlite.initial.sql
                log "Roundcube SQLite database initialized"
            fi
        fi
        chown -R www-data:www-data /var/lib/roundcube
        chmod 750 /var/lib/roundcube
        chmod 640 /var/lib/roundcube/roundcube.db 2>/dev/null
    fi

    # Configure OpenDKIM for DKIM signing
    info "Configuring OpenDKIM..."
    mkdir -p /etc/opendkim/keys
    chown -R opendkim:opendkim /etc/opendkim
    chmod 750 /etc/opendkim

    # Create OpenDKIM configuration files
    touch /etc/opendkim/KeyTable
    touch /etc/opendkim/SigningTable
    cat > /etc/opendkim/TrustedHosts << 'TRUSTED'
127.0.0.1
localhost
::1
TRUSTED
    chown opendkim:opendkim /etc/opendkim/KeyTable /etc/opendkim/SigningTable /etc/opendkim/TrustedHosts
    chmod 644 /etc/opendkim/KeyTable /etc/opendkim/SigningTable /etc/opendkim/TrustedHosts

    # Configure OpenDKIM socket in Postfix chroot
    mkdir -p /var/spool/postfix/opendkim
    chown opendkim:postfix /var/spool/postfix/opendkim
    chmod 750 /var/spool/postfix/opendkim

    # Update OpenDKIM configuration
    if [[ -f /etc/opendkim.conf ]]; then
        # Comment out the default Socket line (we'll add our own)
        sed -i 's|^Socket.*local:/run/opendkim/opendkim.sock|#Socket local:/run/opendkim/opendkim.sock|' /etc/opendkim.conf

        # Add Jabali configuration if not present
        if ! grep -q "^KeyTable" /etc/opendkim.conf; then
            cat >> /etc/opendkim.conf << 'OPENDKIM_CONF'

# Jabali Panel configuration
KeyTable          /etc/opendkim/KeyTable
SigningTable      refile:/etc/opendkim/SigningTable
InternalHosts     /etc/opendkim/TrustedHosts
ExternalIgnoreList /etc/opendkim/TrustedHosts
Socket            local:/var/spool/postfix/opendkim/opendkim.sock
OPENDKIM_CONF
        fi
    fi

    # Configure Postfix to use OpenDKIM milter
    postconf -e "smtpd_milters = unix:opendkim/opendkim.sock"
    postconf -e "non_smtpd_milters = unix:opendkim/opendkim.sock"
    postconf -e "milter_default_action = accept"

    # Add postfix to opendkim group for socket access
    usermod -aG opendkim postfix 2>/dev/null || true

    systemctl enable opendkim

    # Restart mail services to apply configuration
    systemctl restart opendkim
    systemctl restart dovecot
    systemctl restart postfix
    log "OpenDKIM configured"

    log "Mail server configured with SQLite authentication"
}

# Create webmaster mailbox for system notifications
create_webmaster_mailbox() {
    if [[ "$INSTALL_MAIL" != "true" ]]; then
        info "Skipping webmaster mailbox creation (mail server not installed)"
        return
    fi

    header "Creating Webmaster Mailbox"

    local webmaster_password=$(openssl rand -base64 24 | tr -dc 'a-zA-Z0-9!@#$%' | head -c 16)

    # Extract root domain for email (e.g., panel.example.com -> example.com)
    local dot_count=$(echo "$SERVER_HOSTNAME" | tr -cd '.' | wc -c)
    local email_domain="$SERVER_HOSTNAME"
    if [[ $dot_count -gt 1 ]]; then
        email_domain=$(echo "$SERVER_HOSTNAME" | awk -F. '{print $(NF-1)"."$NF}')
    fi

    cd "$JABALI_DIR"
    php artisan tinker --execute="
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

        // Create or find domain for hostname
        \$domain = Domain::firstOrCreate(
            ['domain' => \$hostname],
            ['user_id' => \$admin->id, 'status' => 'active', 'document_root' => '/var/www/html']
        );

        // Enable email for domain if not already
        \$agent = new AgentClient();
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

    log "Webmaster mailbox: webmaster@${SERVER_HOSTNAME}"
    log "Webmaster password: ${webmaster_password}"

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
    header "Configuring DNS Server"

    local server_ip=$(hostname -I | awk '{print $1}')

    # Extract domain from hostname (e.g., panel.example.com -> example.com)
    local domain=$(echo "$SERVER_HOSTNAME" | awk -F. '{if (NF>2) {print $(NF-1)"."$NF} else {print $0}}')
    local hostname_part=$(echo "$SERVER_HOSTNAME" | sed "s/\.$domain$//")

    # If hostname is same as domain (e.g., example.com), use @ for the host part
    if [[ "$hostname_part" == "$domain" ]]; then
        hostname_part="@"
    fi

    info "Setting up DNS zone for: $domain"
    info "Server hostname: $SERVER_HOSTNAME"

    # Create zones directory
    mkdir -p /etc/bind/zones

    # Generate serial (YYYYMMDD01 format)
    local serial=$(date +%Y%m%d)01

    # Create zone file
    cat > /etc/bind/zones/db.$domain << ZONEFILE
\$TTL    3600
@       IN      SOA     ns1.$domain. admin.$domain. (
                        $serial         ; Serial
                        3600            ; Refresh
                        1800            ; Retry
                        604800          ; Expire
                        86400 )         ; Negative Cache TTL

; Name servers
@       IN      NS      ns1.$domain.
@       IN      NS      ns2.$domain.

; A records
@       IN      A       $server_ip
ns1     IN      A       $server_ip
ns2     IN      A       $server_ip
ZONEFILE

    # Add hostname A record if it's a subdomain
    if [[ "$hostname_part" != "@" ]]; then
        echo "$hostname_part     IN      A       $server_ip" >> /etc/bind/zones/db.$domain
    fi

    # Add mail records
    cat >> /etc/bind/zones/db.$domain << MAILRECORDS

; Mail records
@       IN      MX      10 mail.$domain.
mail    IN      A       $server_ip

; SPF record
@       IN      TXT     "v=spf1 mx a ip4:$server_ip ~all"

; DMARC record
_dmarc  IN      TXT     "v=DMARC1; p=none; rua=mailto:admin@$domain"

; Autodiscover for mail clients
autoconfig      IN      A       $server_ip
autodiscover    IN      A       $server_ip
MAILRECORDS

    # Set permissions
    chown bind:bind /etc/bind/zones/db.$domain
    chmod 644 /etc/bind/zones/db.$domain

    # Add zone to named.conf.local if not already present
    if ! grep -q "zone \"$domain\"" /etc/bind/named.conf.local 2>/dev/null; then
        cat >> /etc/bind/named.conf.local << NAMEDCONF

zone "$domain" {
    type master;
    file "/etc/bind/zones/db.$domain";
    allow-transfer { none; };
};
NAMEDCONF
    fi

    # Configure BIND9 options for better security
    if [[ ! -f /etc/bind/named.conf.options.bak ]]; then
        cp /etc/bind/named.conf.options /etc/bind/named.conf.options.bak
        cat > /etc/bind/named.conf.options << 'BINDOPTIONS'
options {
    directory "/var/cache/bind";

    // Forward queries to public DNS if not authoritative
    forwarders {
        8.8.8.8;
        8.8.4.4;
    };

    // Security settings
    dnssec-validation auto;
    auth-nxdomain no;
    listen-on-v6 { any; };

    // Allow queries from anywhere (for authoritative zones)
    allow-query { any; };

    // Disable recursion for security (authoritative only)
    recursion no;
};
BINDOPTIONS
    fi

    # Enable and restart BIND9
    systemctl enable named 2>/dev/null || systemctl enable bind9 2>/dev/null || true
    systemctl restart named 2>/dev/null || systemctl restart bind9 2>/dev/null || true

    log "DNS zone created for $domain"
    info "Zone file: /etc/bind/zones/db.$domain"
    echo ""
    echo -e "${YELLOW}Important DNS Setup:${NC}"
    echo "Point your domain's nameservers to this server:"
    echo "  ns1.$domain -> $server_ip"
    echo "  ns2.$domain -> $server_ip"
    echo ""
    echo "Or add a glue record at your registrar for ns1.$domain"
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

# Configure Firewall
configure_firewall() {
    header "Configuring Firewall"

    if ! command -v ufw &> /dev/null; then
        warn "UFW not found, skipping firewall configuration"
        return
    fi

    ufw --force reset
    ufw default deny incoming
    ufw default allow outgoing
    ufw allow 22/tcp    # SSH
    ufw allow 80/tcp    # HTTP
    ufw allow 443/tcp   # HTTPS

    # Mail ports (only if mail server is being installed)
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        ufw allow 25/tcp    # SMTP
        ufw allow 465/tcp   # SMTPS (SMTP over SSL)
        ufw allow 587/tcp   # Submission (SMTP with STARTTLS)
        ufw allow 110/tcp   # POP3
        ufw allow 143/tcp   # IMAP
        ufw allow 993/tcp   # IMAPS (IMAP over SSL)
        ufw allow 995/tcp   # POP3S (POP3 over SSL)
    fi

    # DNS (only if DNS server is being installed)
    if [[ "$INSTALL_DNS" == "true" ]]; then
        ufw allow 53/tcp    # DNS
        ufw allow 53/udp    # DNS
    fi

    ufw --force enable

    log "Firewall configured"
}

# Configure Security Tools (Fail2ban and ClamAV)
configure_security() {
    header "Configuring Security Tools"

    # Install ModSecurity + CRS (optional)
    if [[ "$INSTALL_SECURITY" == "true" ]]; then
        info "Installing ModSecurity (optional WAF)..."
        local module_pkg=""
        if apt-cache show libnginx-mod-http-modsecurity &>/dev/null; then
            module_pkg="libnginx-mod-http-modsecurity"
        elif apt-cache show libnginx-mod-http-modsecurity2 &>/dev/null; then
            module_pkg="libnginx-mod-http-modsecurity2"
        elif apt-cache show nginx-extras &>/dev/null; then
            module_pkg="nginx-extras"
        else
            warn "ModSecurity nginx module not available in apt repositories"
        fi

        local modsec_lib=""
        if apt-cache show libmodsecurity3t64 &>/dev/null; then
            modsec_lib="libmodsecurity3t64"
        elif apt-cache show libmodsecurity3 &>/dev/null; then
            modsec_lib="libmodsecurity3"
        fi

        local crs_pkg=""
        if apt-cache show modsecurity-crs &>/dev/null; then
            crs_pkg="modsecurity-crs"
        fi

        if [[ -n "$module_pkg" ]]; then
            DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$module_pkg" $modsec_lib $crs_pkg 2>/dev/null || warn "ModSecurity install failed"

            # Ensure ModSecurity base config
            if [[ ! -f /etc/modsecurity/modsecurity.conf ]]; then
                if [[ -f /etc/nginx/modsecurity.conf ]]; then
                    cp /etc/nginx/modsecurity.conf /etc/modsecurity/modsecurity.conf
                elif [[ -f /etc/modsecurity/modsecurity.conf-recommended ]]; then
                    cp /etc/modsecurity/modsecurity.conf-recommended /etc/modsecurity/modsecurity.conf
                elif [[ -f /usr/share/modsecurity-crs/modsecurity.conf-recommended ]]; then
                    cp /usr/share/modsecurity-crs/modsecurity.conf-recommended /etc/modsecurity/modsecurity.conf
                else
                    cat > /etc/modsecurity/modsecurity.conf <<'EOF'
SecRuleEngine DetectionOnly
SecRequestBodyAccess On
SecResponseBodyAccess Off
SecAuditEngine RelevantOnly
SecAuditLog /var/log/nginx/modsec_audit.log
EOF
                fi
            fi

            # Ensure unicode mapping file (required by SecUnicodeMapFile)
            if [[ ! -f /etc/modsecurity/unicode.mapping ]]; then
                if [[ -f /usr/share/modsecurity-crs/util/unicode.mapping ]]; then
                    cp /usr/share/modsecurity-crs/util/unicode.mapping /etc/modsecurity/unicode.mapping
                elif [[ -f /usr/share/modsecurity-crs/unicode.mapping ]]; then
                    cp /usr/share/modsecurity-crs/unicode.mapping /etc/modsecurity/unicode.mapping
                elif [[ -f /usr/share/modsecurity/unicode.mapping ]]; then
                    cp /usr/share/modsecurity/unicode.mapping /etc/modsecurity/unicode.mapping
                elif [[ -f /etc/nginx/unicode.mapping ]]; then
                    cp /etc/nginx/unicode.mapping /etc/modsecurity/unicode.mapping
                elif [[ -f /usr/share/nginx/docs/modsecurity/unicode.mapping ]]; then
                    cp /usr/share/nginx/docs/modsecurity/unicode.mapping /etc/modsecurity/unicode.mapping
                fi
            fi

            # Create main include file for nginx if missing (avoid IncludeOptional)
            mkdir -p /etc/nginx/modsec
            if [[ ! -f /etc/nginx/modsec/main.conf ]]; then
                {
                    echo "Include /etc/modsecurity/modsecurity.conf"
                    if [[ -f /etc/modsecurity/crs/crs-setup.conf ]]; then
                        echo "Include /etc/modsecurity/crs/crs-setup.conf"
                    elif [[ -f /usr/share/modsecurity-crs/crs-setup.conf ]]; then
                        echo "Include /usr/share/modsecurity-crs/crs-setup.conf"
                    fi
                    if [[ -f /etc/modsecurity/crs/REQUEST-900-EXCLUSION-RULES-BEFORE-CRS.conf ]]; then
                        echo "Include /etc/modsecurity/crs/REQUEST-900-EXCLUSION-RULES-BEFORE-CRS.conf"
                    fi
                    if [[ -d /usr/share/modsecurity-crs/rules ]]; then
                        echo "Include /usr/share/modsecurity-crs/rules/*.conf"
                    fi
                    if [[ -f /etc/modsecurity/crs/RESPONSE-999-EXCLUSION-RULES-AFTER-CRS.conf ]]; then
                        echo "Include /etc/modsecurity/crs/RESPONSE-999-EXCLUSION-RULES-AFTER-CRS.conf"
                    fi
                } > /etc/nginx/modsec/main.conf
            fi
        fi
    fi

    # Configure Fail2ban
    info "Configuring Fail2ban..."
    cat > /etc/fail2ban/jail.local << 'FAIL2BAN'
[DEFAULT]
bantime = 600
findtime = 600
maxretry = 5
ignoreip = 127.0.0.1/8 ::1

[sshd]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/auth.log
maxretry = 5

[nginx-http-auth]
enabled = true
port = http,https
filter = nginx-http-auth
logpath = /var/log/nginx/error.log

[nginx-botsearch]
enabled = true
port = http,https
filter = nginx-botsearch
logpath = /var/log/nginx/access.log
maxretry = 2
FAIL2BAN

    # Create WordPress filter and jail
    cat > /etc/fail2ban/filter.d/wordpress.conf << 'WPFILTER'
[Definition]
failregex = ^<HOST> -.*"POST.*/wp-login\.php.*" (200|403)
            ^<HOST> -.*"POST.*/xmlrpc\.php.*" (200|403)
ignoreregex = action=logout
WPFILTER

    cat > /etc/fail2ban/jail.d/wordpress.conf << 'WPJAIL'
[wordpress]
enabled = false
filter = wordpress
port = http,https
logpath = /home/*/domains/*/logs/access.log
          /var/log/nginx/access.log
maxretry = 5
findtime = 300
bantime = 3600
WPJAIL

    # Create mail service jails (disabled by default, enabled when mail is configured)
    cat > /etc/fail2ban/jail.d/dovecot.conf << 'DOVECOTJAIL'
[dovecot]
enabled = false
filter = dovecot
backend = systemd
maxretry = 5
findtime = 300
bantime = 3600
DOVECOTJAIL

    cat > /etc/fail2ban/jail.d/postfix.conf << 'POSTFIXJAIL'
[postfix]
enabled = false
filter = postfix
mode = more
backend = systemd
maxretry = 5
findtime = 300
bantime = 3600

[postfix-sasl]
enabled = false
filter = postfix[mode=auth]
backend = systemd
maxretry = 3
findtime = 300
bantime = 3600
POSTFIXJAIL

    cat > /etc/fail2ban/jail.d/roundcube.conf << 'RCJAIL'
[roundcube-auth]
enabled = false
filter = roundcube-auth
port = http,https
logpath = /var/lib/roundcube/logs/errors.log
maxretry = 5
findtime = 300
bantime = 3600
RCJAIL

    systemctl enable fail2ban
    systemctl restart fail2ban
    log "Fail2ban configured and enabled"

    # Configure SSH Jail for users
    info "Configuring SSH/SFTP jail..."

    # Create groups for SFTP and shell users
    groupadd sftpusers 2>/dev/null || true
    groupadd shellusers 2>/dev/null || true

    # Backup original sshd_config
    cp /etc/ssh/sshd_config /etc/ssh/sshd_config.backup.$(date +%Y%m%d%H%M%S)

    # Remove any existing Jabali SSH config
    sed -i '/# Jabali SSH Jail Configuration/,/# End Jabali SSH Jail/d' /etc/ssh/sshd_config

    # Add SSH jail configuration
    cat >> /etc/ssh/sshd_config << 'SSHJAIL'

# Jabali SSH Jail Configuration
# SFTP-only users (default for all panel users)
Match Group sftpusers
    ChrootDirectory /home/%u
    ForceCommand internal-sftp
    AllowTcpForwarding no
    AllowAgentForwarding no
    PermitTTY no
    X11Forwarding no

# Shell users (jailed with limited commands)
Match Group shellusers
    ChrootDirectory /var/jail
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
# End Jabali SSH Jail
SSHJAIL

    # Restart SSH to apply changes
    systemctl restart sshd
    log "SSH jail configured"

    # Set up jail environment for shell users
    info "Setting up jail environment..."

    # Create jail directory structure
    mkdir -p /var/jail/{bin,lib,lib64,usr,etc,dev,home}
    mkdir -p /var/jail/usr/{bin,lib,share}
    mkdir -p /var/jail/usr/lib/x86_64-linux-gnu

    # Copy essential binaries to jail
    JAIL_BINS="bash sh ls cat cp mv rm mkdir rmdir pwd echo head tail grep sed awk wc sort uniq cut tr touch chmod find which"
    for bin in $JAIL_BINS; do
        if [ -f "/bin/$bin" ]; then
            cp /bin/$bin /var/jail/bin/ 2>/dev/null || true
        elif [ -f "/usr/bin/$bin" ]; then
            cp /usr/bin/$bin /var/jail/usr/bin/ 2>/dev/null || true
        fi
    done

    # Copy wp-cli if available
    if [ -f "/usr/local/bin/wp" ]; then
        cp /usr/local/bin/wp /var/jail/usr/bin/wp
    fi

    # Copy PHP for wp-cli support
    if [ -f "/usr/bin/php" ]; then
        cp /usr/bin/php /var/jail/usr/bin/
    fi

    # Copy env (needed by wp-cli shebang)
    if [ -f "/usr/bin/env" ]; then
        cp /usr/bin/env /var/jail/usr/bin/
    fi

    # Copy required libraries for binaries
    copy_libs_for_binary() {
        local binary="$1"
        if [ -f "$binary" ]; then
            ldd "$binary" 2>/dev/null | grep -o '/[^ ]*' | while read lib; do
                if [ -f "$lib" ]; then
                    local libdir=$(dirname "$lib")
                    mkdir -p "/var/jail$libdir"
                    cp -n "$lib" "/var/jail$libdir/" 2>/dev/null || true
                fi
            done
        fi
    }

    for bin in /var/jail/bin/* /var/jail/usr/bin/*; do
        [ -f "$bin" ] && copy_libs_for_binary "$bin"
    done

    # Copy PHP extensions for wp-cli
    PHP_EXT_DIR=$(php -i 2>/dev/null | grep "^extension_dir" | awk '{print $3}')
    if [ -d "$PHP_EXT_DIR" ]; then
        mkdir -p "/var/jail$PHP_EXT_DIR"
        cp "$PHP_EXT_DIR"/*.so "/var/jail$PHP_EXT_DIR/" 2>/dev/null || true

        # Copy extension library dependencies
        for ext in "$PHP_EXT_DIR"/*.so; do
            [ -f "$ext" ] && copy_libs_for_binary "$ext"
        done
    fi

    # Copy PHP CLI configuration (resolve symlinks to actual files)
    mkdir -p /var/jail/etc/php/8.4/cli/conf.d
    cp /etc/php/8.4/cli/php.ini /var/jail/etc/php/8.4/cli/ 2>/dev/null || true

    # Set timezone in jail's php.ini to avoid warnings
    sed -i 's/;date.timezone =/date.timezone = UTC/' /var/jail/etc/php/8.4/cli/php.ini 2>/dev/null || true

    for f in /etc/php/8.4/cli/conf.d/*.ini; do
        if [ -L "$f" ]; then
            # Resolve symlink and copy actual file
            cp "$(readlink -f "$f")" "/var/jail/etc/php/8.4/cli/conf.d/$(basename "$f")" 2>/dev/null || true
        elif [ -f "$f" ]; then
            cp "$f" "/var/jail/etc/php/8.4/cli/conf.d/" 2>/dev/null || true
        fi
    done

    # Create essential device nodes
    mknod -m 666 /var/jail/dev/null c 1 3 2>/dev/null || true
    mknod -m 666 /var/jail/dev/zero c 1 5 2>/dev/null || true
    mknod -m 666 /var/jail/dev/random c 1 8 2>/dev/null || true
    mknod -m 666 /var/jail/dev/urandom c 1 9 2>/dev/null || true
    mknod -m 666 /var/jail/dev/tty c 5 0 2>/dev/null || true

    # Create minimal /etc files
    grep -E "^(root|nobody)" /etc/passwd > /var/jail/etc/passwd
    grep -E "^(root|nogroup)" /etc/group > /var/jail/etc/group
    cp /etc/nsswitch.conf /var/jail/etc/ 2>/dev/null || true
    cp /etc/hosts /var/jail/etc/ 2>/dev/null || true

    # Copy timezone data for PHP
    mkdir -p /var/jail/usr/share/zoneinfo
    cp -r /usr/share/zoneinfo/* /var/jail/usr/share/zoneinfo/ 2>/dev/null || true
    ln -sf /usr/share/zoneinfo/UTC /var/jail/etc/localtime 2>/dev/null || true

    # Set permissions
    chown root:root /var/jail
    chmod 755 /var/jail

    log "Jail environment configured with wp-cli support"

    # Configure ClamAV only if INSTALL_SECURITY is enabled
    if [[ "$INSTALL_SECURITY" != "true" ]]; then
        log "Skipping ClamAV configuration (security tools not selected)"
        return
    fi

    # Configure ClamAV (disabled by default to save memory)
    info "Configuring ClamAV (disabled by default)..."

    # Create quarantine directory
    mkdir -p /var/lib/clamav/quarantine
    chown clamav:clamav /var/lib/clamav/quarantine
    chmod 750 /var/lib/clamav/quarantine

    # Create optimized ClamAV configuration
    cat > /etc/clamav/clamd.conf << 'CLAMAV_CONF'
# Jabali ClamAV Configuration - Optimized for low resource usage
LocalSocket /var/run/clamav/clamd.ctl
FixStaleSocket true
LocalSocketGroup clamav
LocalSocketMode 666
User clamav
LogFile /var/log/clamav/clamav.log
LogRotate true
LogTime true
DatabaseDirectory /var/lib/clamav
MaxThreads 2
MaxScanSize 25M
MaxFileSize 5M
MaxRecursion 10
ScanArchive false
ScanPE true
ScanELF true
ScanHTML true
AlgorithmicDetection true
PhishingSignatures true
Foreground false
ExitOnOOM true
CLAMAV_CONF

    # Configure freshclam
    cat > /etc/clamav/freshclam.conf << 'FRESHCLAM_CONF'
DatabaseDirectory /var/lib/clamav
UpdateLogFile /var/log/clamav/freshclam.log
LogRotate true
LogTime true
DatabaseMirror database.clamav.net
NotifyClamd /etc/clamav/clamd.conf
Checks 4
FRESHCLAM_CONF

    # Stop ClamAV services - keep them disabled by default
    systemctl stop clamav-daemon 2>/dev/null || true
    systemctl stop clamav-freshclam 2>/dev/null || true
    systemctl disable clamav-daemon 2>/dev/null || true
    systemctl disable clamav-freshclam 2>/dev/null || true

    # Update signatures once during install
    info "Downloading initial virus signatures..."
    freshclam --quiet 2>/dev/null || warn "Could not download virus signatures"

    log "ClamAV configured (disabled by default to save memory)"
    info "Enable ClamAV via Security Center in Admin Panel when needed"
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

    # Create .env file
    cp .env.example .env 2>/dev/null || cat > .env << ENV
APP_NAME=Jabali
APP_ENV=production
APP_KEY=
APP_DEBUG=false
APP_URL=https://$(hostname -I | awk '{print $1}')

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

MAIL_MAILER=sendmail
MAIL_FROM_ADDRESS=webmaster@${SERVER_HOSTNAME}
MAIL_FROM_NAME="Jabali Panel"
ENV

    # Ensure mail settings are correct (in case .env.example was used)
    sed -i "s/^MAIL_MAILER=.*/MAIL_MAILER=sendmail/" .env
    sed -i "s/^MAIL_FROM_ADDRESS=.*/MAIL_FROM_ADDRESS=webmaster@${SERVER_HOSTNAME}/" .env

    # Install dependencies
    log "Installing Composer dependencies..."
    sudo -u $JABALI_USER COMPOSER_ALLOW_SUPERUSER=1 composer install --no-dev --optimize-autoloader
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

    php artisan tinker --execute="
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
    " 2>/dev/null || true

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
        sudo -u "$JABALI_USER" -H env \
            NPM_CONFIG_CACHE="$NPM_CONFIG_CACHE" \
            PUPPETEER_SKIP_DOWNLOAD=1 \
            PUPPETEER_CACHE_DIR="$PUPPETEER_CACHE_DIR" \
            XDG_CACHE_HOME="$XDG_CACHE_HOME" \
            npm install
        sudo -u "$JABALI_USER" -H env \
            NPM_CONFIG_CACHE="$NPM_CONFIG_CACHE" \
            PUPPETEER_SKIP_DOWNLOAD=1 \
            PUPPETEER_CACHE_DIR="$PUPPETEER_CACHE_DIR" \
            XDG_CACHE_HOME="$XDG_CACHE_HOME" \
            npm run build
    else
        npm install
        npm run build
    fi

    # Final permissions - ensure everything is correct after all setup
    chown -R $JABALI_USER:www-data "$JABALI_DIR"
    chown -R www-data:www-data "$JABALI_DIR/storage"
    chown -R www-data:www-data "$JABALI_DIR/bootstrap/cache"
    chmod -R 775 "$JABALI_DIR/storage"
    chmod -R 775 "$JABALI_DIR/bootstrap/cache"

    # Set SQLite database permissions for Dovecot access (if mail server is installed)
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
ExecStart=/usr/bin/php /var/www/jabali/artisan queue:work --sleep=3 --tries=1 --timeout=120
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
        apt-get update -qq
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq cron || true
    fi

    if ! command -v crontab >/dev/null 2>&1; then
        warn "Unable to configure scheduler: crontab command is still missing"
        return
    fi

    # Ensure cron service is enabled and running
    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable cron >/dev/null 2>&1 || true
        systemctl start cron >/dev/null 2>&1 || true
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

# Setup SSL certificates for panel and mail services
setup_panel_ssl() {
    header "Setting Up SSL Certificates for Services"

    # Get public IP (try external service first, fall back to hostname -I)
    local server_ip=$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || curl -s --max-time 5 https://ipv4.icanhazip.com 2>/dev/null || hostname -I | awk '{print $1}')
    server_ip=$(echo "$server_ip" | tr -d '[:space:]')

    # Check if hostname resolves to this server (required for Let's Encrypt)
    local resolved_ip=$(dig +short "$SERVER_HOSTNAME" 2>/dev/null | head -1)

    if [[ -z "$resolved_ip" ]]; then
        warn "Cannot resolve $SERVER_HOSTNAME - skipping Let's Encrypt"
        warn "SSL can be issued later using: certbot --nginx -d $SERVER_HOSTNAME"
        return
    fi

    if [[ "$resolved_ip" != "$server_ip" ]]; then
        warn "Hostname $SERVER_HOSTNAME resolves to $resolved_ip, not this server ($server_ip)"
        warn "SSL can be issued later using: certbot --nginx -d $SERVER_HOSTNAME"
        return
    fi

    info "Attempting to issue Let's Encrypt certificate for $SERVER_HOSTNAME"

    # Issue certificate and configure nginx automatically
    if certbot --nginx -d "$SERVER_HOSTNAME" --non-interactive --agree-tos --email "$ADMIN_EMAIL" --redirect 2>&1; then
        log "Let's Encrypt certificate issued and nginx configured for $SERVER_HOSTNAME"
        export PANEL_SSL_INSTALLED=true
    else
        warn "Could not issue Let's Encrypt certificate for $SERVER_HOSTNAME"
        warn "This may be because the domain is not publicly accessible"
        warn "You can issue manually later: certbot --nginx -d $SERVER_HOSTNAME"
    fi

    # Try to issue certificate for mail hostname if different
    local mail_hostname="mail.$(echo "$SERVER_HOSTNAME" | awk -F. '{if(NF>2){for(i=2;i<=NF;i++)printf "%s%s",$i,(i<NF?".":"")}else print $0}')"

    if [[ "$mail_hostname" != "mail." && "$mail_hostname" != "$SERVER_HOSTNAME" ]]; then
        local mail_resolved=$(dig +short "$mail_hostname" 2>/dev/null | head -1)
        if [[ "$mail_resolved" == "$server_ip" ]]; then
            info "Attempting to issue Let's Encrypt certificate for $mail_hostname"
            if certbot certonly --standalone -d "$mail_hostname" --non-interactive --agree-tos --email "$ADMIN_EMAIL" 2>/dev/null; then
                log "Let's Encrypt certificate issued for $mail_hostname"

                # Update Postfix to use the certificate
                postconf -e "smtpd_tls_cert_file=/etc/letsencrypt/live/$mail_hostname/fullchain.pem"
                postconf -e "smtpd_tls_key_file=/etc/letsencrypt/live/$mail_hostname/privkey.pem"
                systemctl reload postfix 2>/dev/null || true

                # Update Dovecot to use the certificate
                if [[ -f /etc/dovecot/conf.d/10-ssl.conf ]]; then
                    sed -i "s|^ssl_cert = .*|ssl_cert = </etc/letsencrypt/live/$mail_hostname/fullchain.pem|" /etc/dovecot/conf.d/10-ssl.conf
                    sed -i "s|^ssl_key = .*|ssl_key = </etc/letsencrypt/live/$mail_hostname/privkey.pem|" /etc/dovecot/conf.d/10-ssl.conf
                    systemctl reload dovecot 2>/dev/null || true
                fi
                log "Mail services updated to use Let's Encrypt certificate"
            else
                warn "Could not issue certificate for $mail_hostname"
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
        "nginx"
        "mariadb"
        "jabali-agent"
        "jabali-queue"
    )

    # Add PHP-FPM (detect version)
    for version in 8.4 8.3 8.2 8.1 8.0; do
        if systemctl list-unit-files "php${version}-fpm.service" &>/dev/null | grep -q "php${version}-fpm"; then
            services+=("php${version}-fpm")
            break
        fi
    done

    # Add optional services if installed
    if systemctl list-unit-files postfix.service &>/dev/null | grep -q postfix; then
        services+=("postfix")
    fi
    if systemctl list-unit-files dovecot.service &>/dev/null | grep -q dovecot; then
        services+=("dovecot")
    fi
    if systemctl list-unit-files named.service &>/dev/null | grep -q named; then
        services+=("named")
    elif systemctl list-unit-files bind9.service &>/dev/null | grep -q bind9; then
        services+=("bind9")
    fi
    if systemctl list-unit-files redis-server.service &>/dev/null | grep -q redis-server; then
        services+=("redis-server")
    fi
    if systemctl list-unit-files fail2ban.service &>/dev/null | grep -q fail2ban; then
        services+=("fail2ban")
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
        \$user = App\Models\User::create([
            'name' => 'Administrator',
            'username' => 'admin',
            'email' => '${ADMIN_EMAIL}',
            'password' => bcrypt('${ADMIN_PASSWORD}'),
            'is_admin' => true,
        ]);
    " 2>/dev/null || true

    # Save credentials
    echo "ADMIN_EMAIL=${ADMIN_EMAIL}" >> /root/.jabali_db_credentials
    echo "ADMIN_PASSWORD=${ADMIN_PASSWORD}" >> /root/.jabali_db_credentials
}

# Print completion message
print_completion() {
    local server_ip=$(hostname -I | awk '{print $1}')

    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}║       ${BOLD}Jabali Panel Installation Complete!${NC}${GREEN}              ║${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    printf "${GREEN}║${NC}  Version: ${BOLD}%-46s${NC} ${GREEN}║${NC}\n" "v${JABALI_VERSION}"
    printf "${GREEN}║${NC}  Hostname: ${BOLD}%-45s${NC} ${GREEN}║${NC}\n" "${SERVER_HOSTNAME}"
    printf "${GREEN}║${NC}  Server IP: ${BOLD}%-44s${NC} ${GREEN}║${NC}\n" "${server_ip}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
    echo -e "${GREEN}║${NC}  ${BOLD}Admin Credentials:${NC}                                        ${GREEN}║${NC}"
    printf "${GREEN}║${NC}    Email:    ${BOLD}%-43s${NC} ${GREEN}║${NC}\n" "${ADMIN_EMAIL}"
    printf "${GREEN}║${NC}    Password: ${BOLD}%-43s${NC} ${GREEN}║${NC}\n" "${ADMIN_PASSWORD}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
    echo -e "${GREEN}║${NC}  ${BOLD}Panel URLs:${NC}                                               ${GREEN}║${NC}"
    printf "${GREEN}║${NC}    Admin Panel: ${BOLD}%-40s${NC} ${GREEN}║${NC}\n" "https://${SERVER_HOSTNAME}/jabali-admin/"
    printf "${GREEN}║${NC}    User Panel:  ${BOLD}%-40s${NC} ${GREEN}║${NC}\n" "https://${SERVER_HOSTNAME}/jabali-panel/"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
    if [[ "$PANEL_SSL_INSTALLED" == "true" ]]; then
        echo -e "${GREEN}║${NC}  ${GREEN}SSL: Let's Encrypt certificate installed${NC}                 ${GREEN}║${NC}"
    else
        echo -e "${GREEN}║${NC}  ${YELLOW}Note: Using self-signed SSL certificate.${NC}                 ${GREEN}║${NC}"
        echo -e "${GREEN}║${NC}  ${YELLOW}Browser will show a security warning - this is normal.${NC}   ${GREEN}║${NC}"
        echo -e "${GREEN}║                                                            ║${NC}"
        echo -e "${GREEN}║${NC}  ${CYAN}Get a free SSL certificate:${NC}                               ${GREEN}║${NC}"
        printf "${GREEN}║${NC}  ${CYAN}%-56s${NC} ${GREEN}║${NC}\n" "certbot --nginx -d ${SERVER_HOSTNAME}"
    fi
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}║${NC}  CLI Usage: ${CYAN}jabali --help${NC}                                   ${GREEN}║${NC}"
    echo -e "${GREEN}║${NC}  Credentials: ${CYAN}/root/.jabali_db_credentials${NC}                  ${GREEN}║${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

# Uninstall Jabali Panel
uninstall() {
    local force_uninstall=false

    # Check for --force flag
    if [[ "$1" == "--force" ]] || [[ "$1" == "-f" ]]; then
        force_uninstall=true
    fi

    show_banner
    check_root

    echo -e "${RED}${BOLD}WARNING: This will completely remove Jabali Panel and all related services!${NC}"
    echo ""
    echo "This will remove:"
    echo "  - Jabali Panel files (/var/www/jabali)"
    echo "  - Jabali database and user"
    echo "  - Nginx, PHP-FPM, MariaDB, Redis"
    echo "  - Mail server (Postfix, Dovecot, Rspamd)"
    echo "  - DNS server (BIND9)"
    echo "  - All user home directories (/home/*)"
    echo "  - All virtual mail (/var/mail)"
    echo "  - All domains and configurations"
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

    systemctl stop sysstat-collect.timer 2>/dev/null || true
    systemctl disable sysstat-collect.timer 2>/dev/null || true
    systemctl stop sysstat-summary.timer 2>/dev/null || true
    systemctl disable sysstat-summary.timer 2>/dev/null || true
    systemctl stop sysstat-rotate.timer 2>/dev/null || true
    systemctl disable sysstat-rotate.timer 2>/dev/null || true
    systemctl stop sysstat 2>/dev/null || true
    systemctl disable sysstat 2>/dev/null || true
    rm -rf /etc/systemd/system/sysstat-collect.timer.d
    rm -rf /etc/systemd/system/sysstat-summary.timer.d

    local services=(
        nginx
        php-fpm
        php8.4-fpm
        mariadb
        mysql
        redis-server
        postfix
        dovecot
        rspamd
        opendkim
        bind9
        named
        fail2ban
        clamav-daemon
        clamav-freshclam
        sysstat
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
    rm -rf /etc/systemd/system/named.service.d/restart.conf
    rm -rf /etc/systemd/system/bind9.service.d/restart.conf
    rm -rf /etc/systemd/system/redis-server.service.d/restart.conf
    rm -rf /etc/systemd/system/fail2ban.service.d/restart.conf

    systemctl daemon-reload

    header "Removing Jabali Panel"
    rm -rf "$JABALI_DIR"
    rm -rf /var/www/jabali.bk
    rm -rf /var/www/jabali.bak.*
    rm -f /usr/local/bin/jabali
    rm -rf /var/run/jabali
    rm -rf /var/log/jabali
    rm -rf /var/backups/jabali
    rm -rf /etc/jabali
    rm -rf /etc/ssl/jabali
    rm -f /etc/security/jabali-ssh-notify.sh
    rm -f /usr/local/bin/wp
    rm -f /root/.jabali_db_credentials
    rm -f /root/.jabali_redis_credentials
    log "Jabali Panel removed"

    header "Removing Database"
    mysql -e "DROP DATABASE IF EXISTS jabali;" 2>/dev/null || true
    mysql -e "DROP USER IF EXISTS 'jabali'@'localhost';" 2>/dev/null || true
    log "Database removed"

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
        bind9
        bind9-utils

        # Webmail
        roundcube
        roundcube-core
        roundcube-mysql
        roundcube-sqlite3
        roundcube-plugins

        # Security
        fail2ban
        libnginx-mod-http-modsecurity
        libnginx-mod-http-modsecurity2
        libmodsecurity3t64
        libmodsecurity3
        modsecurity-crs
        nginx-extras
        clamav
        clamav-daemon
        clamav-freshclam

        # Metrics
        sysstat

        # Additional components installed by Jabali
        nodejs
        geoipupdate
        libnginx-mod-http-geoip2
        chromium
        build-essential
        quota
        goaccess
        lynis
        ruby
        ruby-dev
        sqlite3
        ufw
    )

    for pkg in "${packages[@]}"; do
        DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq $pkg 2>/dev/null || true
    done

    DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq
    DEBIAN_FRONTEND=noninteractive apt-get autoclean -y -qq

    log "Packages removed"

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

    # Security
    rm -rf /etc/fail2ban
    rm -rf /var/lib/fail2ban
    rm -rf /var/log/fail2ban.log*
    rm -rf /var/lib/clamav
    rm -rf /var/log/clamav
    rm -rf /etc/geoipupdate
    rm -rf /etc/GeoIP.conf
    rm -rf /usr/share/GeoIP
    rm -rf /var/lib/GeoIP
    rm -f /usr/local/bin/geoipupdate
    rm -rf /opt/nikto
    rm -f /usr/local/bin/nikto
    rm -f /usr/local/bin/wpscan
    rm -rf /var/lib/gems
    rm -rf /var/cache/gem

    # Metrics
    rm -rf /etc/sysstat
    rm -rf /etc/default/sysstat
    rm -rf /var/log/sysstat

    # SSL certificates (Let's Encrypt)
    rm -rf /etc/letsencrypt

    # PHP repository
    rm -f /etc/apt/sources.list.d/php.list
    rm -f /usr/share/keyrings/sury-php.gpg
    # NodeSource repository
    rm -f /etc/apt/sources.list.d/nodesource.list
    rm -f /usr/share/keyrings/nodesource.gpg
    # Jabali contrib repository
    rm -f /etc/apt/sources.list.d/jabali-contrib.list

    # Jabali-specific configs
    rm -rf /var/backups/users
    rm -f /etc/logrotate.d/jabali-users

    # Remove www-data cron jobs (Laravel scheduler)
    if command -v crontab >/dev/null 2>&1; then
        crontab -u www-data -r 2>/dev/null || true
    fi

    log "Configuration files cleaned"

    header "Removing User Data"
    read -p "Remove all user home directories? (y/N): " remove_homes < /dev/tty
    if [[ "$remove_homes" =~ ^[Yy]$ ]]; then
        # Get list of normal users (UID >= 1000, excluding nobody)
        for user_home in /home/*; do
            if [[ -d "$user_home" ]]; then
                username=$(basename "$user_home")
                userdel -r "$username" 2>/dev/null || rm -rf "$user_home"
                log "Removed user: $username"
            fi
        done
    fi

    # Remove vmail user
    userdel vmail 2>/dev/null || true
    groupdel vmail 2>/dev/null || true

    header "Resetting Firewall"
    if command -v ufw &> /dev/null; then
        ufw --force reset
        ufw --force disable
    else
        info "UFW not installed, skipping firewall reset"
    fi

    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}║       ${BOLD}Jabali Panel Uninstallation Complete!${NC}${GREEN}             ║${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}║${NC}  All Jabali Panel components have been removed.            ${GREEN}║${NC}"
    echo -e "${GREEN}║${NC}  Your server is now clean.                                 ${GREEN}║${NC}"
    echo -e "${GREEN}║                                                            ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
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
    echo ""
    echo "Commands:"
    echo "  install              Install Jabali Panel (default, interactive)"
    echo "  uninstall [--force]  Remove Jabali Panel and all components"
    echo "  --help               Show this help message"
    echo ""
    echo "Environment Variables (for non-interactive install):"
    echo "  SERVER_HOSTNAME      Set the server hostname"
    echo "  JABALI_FULL          Install all components (set to any value)"
    echo "  JABALI_MINIMAL       Install only core components (set to any value)"
    echo ""
    echo "Installation Modes:"
    echo "  Full Installation    - Web, Mail, DNS, Firewall, Security tools"
    echo "  Minimal Installation - Web server only (Nginx, PHP, MariaDB, Redis, UFW)"
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
    echo "  Install using Gitea-hosted installer:"
    echo "    curl -fsSL http://gitea.example.com:3001/shukivaknin/jabali-panel/raw/branch/main/install.sh | sudo bash"
    echo ""
    echo "  Uninstall:"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash -s -- uninstall"
    echo ""
    echo "  Force uninstall (no prompts):"
    echo "    curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash -s -- uninstall --force"
    echo ""
}

# Main installation
main() {
    show_banner
    check_root
    check_os

    local queue_was_active=false
    if systemctl is-active --quiet jabali-queue; then
        queue_was_active=true
        systemctl stop jabali-queue 2>/dev/null || true
    fi

    prompt_hostname
    select_features

    add_repositories
    install_packages
    configure_sysstat
    install_geoipupdate_binary
    install_composer
    install_wp_cli
    clone_jabali
    configure_php
    configure_mariadb
    configure_nginx

    # Optional components based on feature selection
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        configure_mail
    else
        info "Skipping Mail Server configuration"
    fi

    if [[ "$INSTALL_DNS" == "true" ]]; then
        configure_dns
    else
        info "Skipping DNS Server configuration"
    fi

    if [[ "$INSTALL_FIREWALL" == "true" ]]; then
        configure_firewall
    else
        info "Skipping Firewall configuration"
    fi

    # Setup disk quotas for user space management
    setup_quotas

    # Always configure fail2ban and SSH jails (ClamAV is conditional inside)
    configure_security

    # Enable fail2ban mail jails if mail server was installed
    if [[ "$INSTALL_MAIL" == "true" ]]; then
        info "Enabling fail2ban mail protection jails..."
        if [[ -f /etc/fail2ban/jail.d/dovecot.conf ]]; then
            sed -i 's/^enabled = false/enabled = true/' /etc/fail2ban/jail.d/dovecot.conf
        fi
        if [[ -f /etc/fail2ban/jail.d/postfix.conf ]]; then
            sed -i 's/^enabled = false/enabled = true/' /etc/fail2ban/jail.d/postfix.conf
        fi
        # Only enable roundcube jail if log file exists
        if [[ -f /etc/fail2ban/jail.d/roundcube.conf ]]; then
            # Create roundcube logs directory if roundcube is installed
            if [[ -d /var/lib/roundcube ]]; then
                mkdir -p /var/lib/roundcube/logs
                touch /var/lib/roundcube/logs/errors.log
                chown -R www-data:www-data /var/lib/roundcube/logs
            fi
            # Only enable if log file exists
            if [[ -f /var/lib/roundcube/logs/errors.log ]]; then
                sed -i 's/^enabled = false/enabled = true/' /etc/fail2ban/jail.d/roundcube.conf
            fi
        fi
        systemctl reload fail2ban 2>/dev/null || systemctl restart fail2ban 2>/dev/null || true
        log "Fail2ban mail jails enabled"
    fi

    configure_redis
    setup_jabali
    setup_agent_service
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

    print_completion
}

# Parse command line arguments
COMMAND="install"
UNINSTALL_FORCE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        install|uninstall|remove|purge|--help|-h|help)
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
        --force|-f)
            UNINSTALL_FORCE="--force"
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
    uninstall|remove|purge)
        uninstall "$UNINSTALL_FORCE"
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
