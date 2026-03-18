# ── Stage 1: builder ───────────────────────────────────────────────
FROM debian:bookworm-slim AS builder

ARG DEBIAN_FRONTEND=noninteractive
ARG PHP_VERSION=8.4

# Install build dependencies: PHP 8.4 CLI + extensions from sury.org, Node 20, Composer
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg lsb-release software-properties-common \
    && curl -sSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /usr/share/keyrings/sury-php.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ $(lsb_release -sc) main" > /etc/apt/sources.list.d/sury-php.list \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get update && apt-get install -y --no-install-recommends \
    php${PHP_VERSION}-cli php${PHP_VERSION}-common php${PHP_VERSION}-curl \
    php${PHP_VERSION}-mbstring php${PHP_VERSION}-xml php${PHP_VERSION}-zip \
    php${PHP_VERSION}-bcmath php${PHP_VERSION}-intl php${PHP_VERSION}-mysql \
    php${PHP_VERSION}-sqlite3 php${PHP_VERSION}-gd php${PHP_VERSION}-redis \
    php${PHP_VERSION}-tokenizer \
    nodejs unzip git \
    && rm -rf /var/lib/apt/lists/*

# Install Composer
RUN curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer

WORKDIR /app
COPY composer.json composer.lock ./

# Composer install with auth.json secret for Filament private repo
RUN --mount=type=secret,id=composer_auth,target=/app/auth.json \
    composer install --no-dev --no-scripts --no-interaction --prefer-dist --optimize-autoloader

COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build
RUN composer run-script post-autoload-dump 2>/dev/null || true

# ── Stage 2: runtime ──────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

ARG DEBIAN_FRONTEND=noninteractive
ARG PHP_VERSION=8.4

LABEL org.opencontainers.image.title="Jabali Panel" \
      org.opencontainers.image.description="All-in-one hosting control panel" \
      org.opencontainers.image.vendor="Jabali" \
      org.opencontainers.image.source="https://github.com/shukiv/jabali-panel"

# Install ALL runtime packages in a single layer
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg lsb-release software-properties-common \
    && curl -sSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /usr/share/keyrings/sury-php.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ $(lsb_release -sc) main" > /etc/apt/sources.list.d/sury-php.list \
    && apt-get update && apt-get install -y --no-install-recommends \
    # PHP 8.4 FPM + extensions
    php${PHP_VERSION}-fpm php${PHP_VERSION}-cli php${PHP_VERSION}-common \
    php${PHP_VERSION}-mysql php${PHP_VERSION}-pgsql php${PHP_VERSION}-sqlite3 \
    php${PHP_VERSION}-redis php${PHP_VERSION}-curl php${PHP_VERSION}-gd \
    php${PHP_VERSION}-mbstring php${PHP_VERSION}-xml php${PHP_VERSION}-zip \
    php${PHP_VERSION}-bcmath php${PHP_VERSION}-intl php${PHP_VERSION}-readline \
    php${PHP_VERSION}-soap php${PHP_VERSION}-imap php${PHP_VERSION}-ldap \
    php${PHP_VERSION}-imagick php${PHP_VERSION}-opcache \
    # Web server
    nginx \
    # Database
    mariadb-server redis-server sqlite3 \
    # Mail
    postfix dovecot-imapd dovecot-pop3d dovecot-lmtpd dovecot-mysql \
    opendkim opendkim-tools rspamd \
    # DNS
    bind9 bind9utils \
    # Security
    fail2ban certbot python3-certbot-nginx \
    # Tools
    supervisor cron openssl curl procps \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/run/jabali /var/log/jabali /var/run/php /var/mail \
    && groupadd -g 5000 vmail || true \
    && useradd -u 5000 -g vmail -d /var/mail -s /usr/sbin/nologin vmail || true

# Stalwart Mail Server (downloaded at build time, activated by MAIL_BACKEND=stalwart)
ARG TARGETARCH
RUN RUST_ARCH=$(case "$(dpkg --print-architecture)" in amd64) echo x86_64;; arm64) echo aarch64;; armhf) echo armv7;; *) echo unknown;; esac) \
    && curl -fsSL "https://github.com/stalwartlabs/mail-server/releases/latest/download/stalwart-${RUST_ARCH}-unknown-linux-gnu.tar.gz" \
    | tar -xz -C /usr/local/bin/ stalwart \
    && chmod 755 /usr/local/bin/stalwart \
    && mkdir -p /etc/stalwart-mail /var/lib/stalwart-mail /var/log/stalwart-mail

# Bind MariaDB to localhost only
RUN printf '[mysqld]\nbind-address = 127.0.0.1\n' > /etc/mysql/mariadb.conf.d/99-container.cnf

# Copy app from builder
COPY --from=builder /app /var/www/jabali
# Remove build artifacts not needed at runtime
RUN rm -rf /var/www/jabali/node_modules /var/www/jabali/.git

# Copy container configs
COPY docker/nginx-panel.conf /etc/nginx/sites-available/jabali-panel
COPY docker/php-fpm-panel.conf /etc/php/${PHP_VERSION}/fpm/php-fpm-panel.conf
COPY docker/php-fpm-panel-pool.conf /etc/php/${PHP_VERSION}/fpm/pool.d/panel.conf
COPY docker/supervisord.conf /etc/supervisor/conf.d/jabali.conf
COPY docker/container-entrypoint.sh /usr/local/bin/container-entrypoint.sh

# Setup nginx
RUN rm -f /etc/nginx/sites-enabled/default \
    && ln -sf /etc/nginx/sites-available/jabali-panel /etc/nginx/sites-enabled/jabali-panel

# Make entrypoint executable
RUN chmod +x /usr/local/bin/container-entrypoint.sh

# Permissions
RUN chown -R www-data:www-data /var/www/jabali/storage /var/www/jabali/bootstrap/cache \
    && chmod -R ug+rwX /var/www/jabali/storage /var/www/jabali/bootstrap/cache

EXPOSE 80 443 25 587 993 110 53/tcp 53/udp 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD curl -sf http://localhost/up || exit 1

ENTRYPOINT ["/usr/local/bin/container-entrypoint.sh"]
