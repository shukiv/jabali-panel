#!/usr/bin/env sh
set -eu

cd /var/www/jabali

mkdir -p storage/framework/cache storage/framework/sessions storage/framework/views storage/logs bootstrap/cache

if [ ! -f .env ]; then
  cp .env.example .env
fi

if [ -n "${APP_URL:-}" ]; then
  sed -i "s|^APP_URL=.*|APP_URL=${APP_URL}|" .env
fi

if [ -n "${ASSET_URL:-}" ]; then
  sed -i "s|^ASSET_URL=.*|ASSET_URL=${ASSET_URL}|" .env
fi

php -r "if (trim(getenv('APP_KEY') ?: '') === '') { echo 'Generating APP_KEY...\n'; }"
php artisan key:generate --force >/dev/null 2>&1 || true

php artisan storage:link >/dev/null 2>&1 || true

chmod -R ug+rw storage bootstrap/cache

exec php -S 0.0.0.0:5555 -t public
