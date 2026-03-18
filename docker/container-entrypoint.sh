#!/usr/bin/env bash
set -euo pipefail

PHP_VERSION="${PHP_VERSION:-8.4}"
APP_DIR="/var/www/jabali"
FIRST_RUN_MARKER="/var/lib/mysql/.jabali-initialized"

export PHP_VERSION

echo "==> Jabali Panel Container Starting..."

# ── 1. Create runtime directories ──────────────────────────────────
mkdir -p /var/run/jabali /var/log/jabali /var/run/php \
         /var/mail /var/spool/postfix/opendkim \
         /var/log/supervisor /run/named \
         "${APP_DIR}/storage/app/public" \
         "${APP_DIR}/storage/framework/cache/data" \
         "${APP_DIR}/storage/framework/sessions" \
         "${APP_DIR}/storage/framework/views" \
         "${APP_DIR}/storage/logs" \
         "${APP_DIR}/bootstrap/cache"

# Create log files needed by fail2ban
touch /var/log/auth.log /var/log/nginx/access.log /var/log/nginx/error.log 2>/dev/null || true

# Disable sshd jail (no SSH in container)
mkdir -p /etc/fail2ban/jail.d
printf '[sshd]\nenabled = false\n' > /etc/fail2ban/jail.d/00-container.conf

# ── 2. Initialize MariaDB if first run ─────────────────────────────
if [ ! -f "$FIRST_RUN_MARKER" ]; then
    echo "==> First run: initializing MariaDB..."
    mysql_install_db --user=mysql --datadir=/var/lib/mysql > /dev/null 2>&1

    # Start MariaDB temporarily
    mysqld_safe --skip-networking &
    MYSQL_PID=$!

    # Wait for MariaDB to be ready
    for i in $(seq 1 30); do
        if mysqladmin ping --silent 2>/dev/null; then
            break
        fi
        sleep 1
    done

    # Generate secure credentials
    DB_PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)

    # Create database and user
    mysql -u root <<-EOSQL
        CREATE DATABASE IF NOT EXISTS jabali CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
        CREATE USER IF NOT EXISTS 'jabali'@'localhost' IDENTIFIED BY '${DB_PASSWORD}';
        CREATE USER IF NOT EXISTS 'jabali'@'127.0.0.1' IDENTIFIED BY '${DB_PASSWORD}';
        GRANT ALL PRIVILEGES ON jabali.* TO 'jabali'@'localhost';
        GRANT ALL PRIVILEGES ON jabali.* TO 'jabali'@'127.0.0.1';
        DELETE FROM mysql.user WHERE User='';
        DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost', '127.0.0.1', '::1');
        DROP DATABASE IF EXISTS test;
        FLUSH PRIVILEGES;
EOSQL

    # Save credentials
    cat > /root/.jabali_db_credentials <<-EOF
        DB_DATABASE=jabali
        DB_USERNAME=jabali
        DB_PASSWORD=${DB_PASSWORD}
EOF
    chmod 600 /root/.jabali_db_credentials

    # Stop temporary MariaDB
    mysqladmin shutdown 2>/dev/null || kill "$MYSQL_PID" 2>/dev/null || true
    wait "$MYSQL_PID" 2>/dev/null || true

    touch "$FIRST_RUN_MARKER"
    echo "==> MariaDB initialized."
fi

# ── 3. Generate Redis password if first run ────────────────────────
REDIS_CONF="/etc/redis/redis.conf"
if ! grep -q "^requirepass " "$REDIS_CONF" 2>/dev/null; then
    REDIS_PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 32)
    echo "requirepass ${REDIS_PASSWORD}" >> "$REDIS_CONF"
    echo "REDIS_PASSWORD=${REDIS_PASSWORD}" >> /root/.jabali_db_credentials
    # Bind to localhost only
    sed -i 's/^bind .*/bind 127.0.0.1 ::1/' "$REDIS_CONF"
    echo "==> Redis password configured."
fi

# Read saved credentials
source /root/.jabali_db_credentials 2>/dev/null || true

# ── 4. Create .env from template ───────────────────────────────────
if [ ! -f "${APP_DIR}/.env" ]; then
    echo "==> Creating .env from template..."
    cp "${APP_DIR}/.env.example" "${APP_DIR}/.env"

    # Set container defaults
    sed -i "s|^APP_NAME=.*|APP_NAME=Jabali|" "${APP_DIR}/.env"
    sed -i "s|^APP_ENV=.*|APP_ENV=production|" "${APP_DIR}/.env"
    sed -i "s|^APP_DEBUG=.*|APP_DEBUG=false|" "${APP_DIR}/.env"
    sed -i "s|^LOG_LEVEL=.*|LOG_LEVEL=error|" "${APP_DIR}/.env"
    sed -i "s|^DB_CONNECTION=.*|DB_CONNECTION=mysql|" "${APP_DIR}/.env"
    sed -i "s|^# DB_HOST=.*|DB_HOST=127.0.0.1|" "${APP_DIR}/.env"
    sed -i "s|^# DB_PORT=.*|DB_PORT=3306|" "${APP_DIR}/.env"
    sed -i "s|^# DB_DATABASE=.*|DB_DATABASE=jabali|" "${APP_DIR}/.env"
    sed -i "s|^# DB_USERNAME=.*|DB_USERNAME=jabali|" "${APP_DIR}/.env"
    # Also handle non-commented versions
    sed -i "s|^DB_HOST=.*|DB_HOST=127.0.0.1|" "${APP_DIR}/.env"
    sed -i "s|^DB_PORT=.*|DB_PORT=3306|" "${APP_DIR}/.env"
    sed -i "s|^DB_DATABASE=.*|DB_DATABASE=jabali|" "${APP_DIR}/.env"
    sed -i "s|^DB_USERNAME=.*|DB_USERNAME=jabali|" "${APP_DIR}/.env"

    if [ -n "${DB_PASSWORD:-}" ]; then
        # Use pipe delimiter since password may contain special chars
        sed -i "s|^# DB_PASSWORD=.*|DB_PASSWORD=${DB_PASSWORD}|" "${APP_DIR}/.env"
        sed -i "s|^DB_PASSWORD=.*|DB_PASSWORD=${DB_PASSWORD}|" "${APP_DIR}/.env"
    fi

    if [ -n "${REDIS_PASSWORD:-}" ]; then
        sed -i "s|^REDIS_PASSWORD=.*|REDIS_PASSWORD=${REDIS_PASSWORD}|" "${APP_DIR}/.env"
    fi

    sed -i "s|^CACHE_STORE=.*|CACHE_STORE=redis|" "${APP_DIR}/.env"
    sed -i "s|^SESSION_DRIVER=.*|SESSION_DRIVER=redis|" "${APP_DIR}/.env"
    sed -i "s|^QUEUE_CONNECTION=.*|QUEUE_CONNECTION=redis|" "${APP_DIR}/.env"
    sed -i "s|^MAIL_MAILER=.*|MAIL_MAILER=smtp|" "${APP_DIR}/.env"
    sed -i "s|^MAIL_HOST=.*|MAIL_HOST=127.0.0.1|" "${APP_DIR}/.env"
    sed -i "s|^MAIL_PORT=.*|MAIL_PORT=587|" "${APP_DIR}/.env"
fi

# ── 5. Override .env with runtime env vars ─────────────────────────
apply_env_override() {
    local var_name="$1"
    local var_value="${!var_name:-}"
    if [ -n "$var_value" ]; then
        # Remove existing line and append new value
        grep -v "^${var_name}=" "${APP_DIR}/.env" > "${APP_DIR}/.env.tmp" || true
        printf '%s=%s\n' "$var_name" "$var_value" >> "${APP_DIR}/.env.tmp"
        mv "${APP_DIR}/.env.tmp" "${APP_DIR}/.env"
    fi
}

# Apply overrides from runtime environment variables
for var in APP_URL APP_KEY APP_ENV APP_DEBUG APP_NAME \
           DB_CONNECTION DB_HOST DB_PORT DB_DATABASE DB_USERNAME DB_PASSWORD \
           REDIS_HOST REDIS_PASSWORD REDIS_PORT \
           MAIL_MAILER MAIL_HOST MAIL_PORT MAIL_USERNAME MAIL_PASSWORD MAIL_FROM_ADDRESS MAIL_BACKEND \
           TRUSTED_PROXIES SERVER_HOSTNAME \
           JABALI_AGENT_SOCKET JABALI_AGENT_TIMEOUT JABALI_INTERNAL_API_TOKEN; do
    apply_env_override "$var"
done

# ── 6. Generate APP_KEY if missing ─────────────────────────────────
if ! grep -q "^APP_KEY=base64:" "${APP_DIR}/.env"; then
    echo "==> Generating APP_KEY..."
    cd "${APP_DIR}" && php artisan key:generate --force --no-interaction
fi

# ── 7. Set permissions ─────────────────────────────────────────────
chown -R www-data:www-data "${APP_DIR}/storage" "${APP_DIR}/bootstrap/cache"
chmod -R ug+rwX "${APP_DIR}/storage" "${APP_DIR}/bootstrap/cache"
chown mysql:mysql /var/lib/mysql
chown -R vmail:vmail /var/mail 2>/dev/null || true

# ── 8. Run migrations ─────────────────────────────────────────────
echo "==> Running database migrations..."

# Start MariaDB temporarily for migrations (with TCP on localhost for Laravel)
mysqld_safe --bind-address=127.0.0.1 &
MYSQL_PID=$!
for i in $(seq 1 30); do
    if mysqladmin ping --silent 2>/dev/null; then break; fi
    sleep 1
done

cd "${APP_DIR}" && php artisan migrate --force --no-interaction

# Stop temporary MariaDB
mysqladmin shutdown 2>/dev/null || kill "$MYSQL_PID" 2>/dev/null || true
wait "$MYSQL_PID" 2>/dev/null || true

# ── 9. Storage link ───────────────────────────────────────────────
cd "${APP_DIR}" && php artisan storage:link --force --no-interaction 2>/dev/null || true

# ── 10. Cache config/routes/views ──────────────────────────────────
echo "==> Caching configuration..."
cd "${APP_DIR}" && php artisan config:cache --no-interaction
cd "${APP_DIR}" && php artisan route:cache --no-interaction
cd "${APP_DIR}" && php artisan view:cache --no-interaction

# ── 11. Setup crontab ─────────────────────────────────────────────
echo "* * * * * cd ${APP_DIR} && php artisan schedule:run >> /dev/null 2>&1" | crontab -u www-data -

# ── 11b. Validate SERVER_HOSTNAME ─────────────────────────────────
if [ -n "${SERVER_HOSTNAME:-}" ]; then
    if ! echo "$SERVER_HOSTNAME" | grep -qP '^[a-zA-Z0-9][a-zA-Z0-9._-]*$'; then
        echo "ERROR: SERVER_HOSTNAME contains invalid characters: ${SERVER_HOSTNAME}"
        exit 1
    fi
fi

# ── 12. Generate self-signed SSL if none provided ──────────────────
SSL_DIR="/etc/ssl/jabali"
mkdir -p "$SSL_DIR"
if [ ! -f "${SSL_DIR}/panel.crt" ] || [ ! -f "${SSL_DIR}/panel.key" ]; then
    CERT_HOSTNAME="${SERVER_HOSTNAME:-localhost}"
    echo "==> Generating self-signed SSL certificate for ${CERT_HOSTNAME}..."
    openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
        -keyout "${SSL_DIR}/panel.key" \
        -out "${SSL_DIR}/panel.crt" \
        -subj "/CN=${CERT_HOSTNAME}" \
        -addext "subjectAltName=DNS:${CERT_HOSTNAME},DNS:localhost,IP:127.0.0.1" \
        2>/dev/null
fi

# ── 13. Configure mail backend ─────────────────────────────────────
MAIL_BACKEND="${MAIL_BACKEND:-legacy}"

if [[ "$MAIL_BACKEND" == "stalwart" ]]; then
    echo "[jabali] Mail backend: Stalwart Mail Server"

    # Enable Stalwart, disable legacy mail services
    sed -i '/^\[program:stalwart-mail\]/,/^\[/ s/autostart=false/autostart=true/' /etc/supervisor/conf.d/jabali.conf

    # Disable legacy services
    for svc in postfix dovecot opendkim rspamd; do
        sed -i "/^\[program:${svc}\]/,/^\[/ s/autostart=true/autostart=false/" /etc/supervisor/conf.d/jabali.conf
    done

    # Generate Stalwart config if not present
    if [[ ! -f /etc/stalwart-mail/config.toml ]]; then
        echo "[jabali] Generating Stalwart configuration..."
        # Will be generated by install.sh or first-run setup
    fi

    # Configure hostname in Stalwart
    if [[ -n "${SERVER_HOSTNAME:-}" ]]; then
        if [[ -f /etc/stalwart-mail/config.toml ]]; then
            sed -i "s/^hostname = .*/hostname = \"${SERVER_HOSTNAME}\"/" /etc/stalwart-mail/config.toml
        fi
    fi

    mkdir -p /var/log/stalwart-mail /var/lib/stalwart-mail/data /var/lib/stalwart-mail/blobs /var/lib/stalwart-mail/queue
else
    echo "[jabali] Mail backend: Legacy (Postfix+Dovecot+OpenDKIM+Rspamd)"

    # Ensure Stalwart is disabled
    sed -i '/^\[program:stalwart-mail\]/,/^\[/ s/autostart=true/autostart=false/' /etc/supervisor/conf.d/jabali.conf 2>/dev/null || true

    # Legacy Postfix hostname configuration (existing behavior)
    if [[ -n "${SERVER_HOSTNAME:-}" ]]; then
        postconf -e "myhostname=${SERVER_HOSTNAME}" 2>/dev/null || true
        postconf -e "mydomain=${SERVER_HOSTNAME#*.}" 2>/dev/null || true
    fi
fi

echo "==> Starting services via supervisord..."
exec /usr/bin/supervisord -c /etc/supervisor/conf.d/jabali.conf
