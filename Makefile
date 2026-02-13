# Jabali Web Hosting Panel - Development Makefile

.PHONY: dev test lint fix fresh migrate seed install build clean agent-restart agent-logs

# Development
dev:
	composer dev

serve:
	php artisan serve

queue:
	php artisan queue:listen --tries=1

logs:
	php artisan pail --timeout=0

# Testing
test:
	php artisan test

test-filter:
	@read -p "Filter: " filter && php artisan test --filter=$$filter

test-coverage:
	php artisan test --coverage

# Code Quality
lint:
	./vendor/bin/pint --test

fix:
	./vendor/bin/pint

analyze:
	./vendor/bin/phpstan analyse --memory-limit=512M 2>/dev/null || echo "PHPStan not installed"

# Database
migrate:
	php artisan migrate

migrate-fresh:
	php artisan migrate:fresh

seed:
	php artisan db:seed

fresh: migrate-fresh seed

rollback:
	php artisan migrate:rollback

# Build
build:
	npm run build

build-dev:
	npm run dev

install:
	composer install
	npm install

update:
	composer update
	npm update

# Cache
cache:
	php artisan config:cache
	php artisan route:cache
	php artisan view:cache

clear:
	php artisan config:clear
	php artisan route:clear
	php artisan view:clear
	php artisan cache:clear

# Jabali Agent
agent-restart:
	sudo systemctl restart jabali-agent

agent-status:
	sudo systemctl status jabali-agent

agent-logs:
	sudo tail -f /var/log/jabali/agent.log

agent-test:
	@echo '{"action":"ping"}' | sudo socat - UNIX-CONNECT:/var/run/jabali/agent.sock

# Filament
filament-assets:
	php artisan filament:assets

# Tinker
tinker:
	php artisan tinker

# Cleanup
clean:
	rm -rf node_modules
	rm -rf vendor
	rm -rf bootstrap/cache/*.php
	rm -rf storage/framework/cache/data/*
	rm -rf storage/framework/sessions/*
	rm -rf storage/framework/views/*

# Help
help:
	@echo "Available targets:"
	@echo "  dev          - Start development servers (serve, queue, pail, vite)"
	@echo "  test         - Run PHPUnit tests"
	@echo "  lint         - Check code style with Pint"
	@echo "  fix          - Fix code style with Pint"
	@echo "  migrate      - Run database migrations"
	@echo "  fresh        - Fresh migrate and seed"
	@echo "  build        - Build frontend assets"
	@echo "  cache        - Cache config, routes, views"
	@echo "  clear        - Clear all caches"
	@echo "  agent-*      - Jabali agent management"
