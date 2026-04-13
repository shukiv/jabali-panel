#!/usr/bin/env bash
set -euo pipefail

# Install the jabali-backup panel addon into the main Jabali panel tree.
#
# Copies Filament pages, the App\Backup namespace (Adapters, Concerns,
# ServiceProvider), views, and the public download endpoint, then warms
# Laravel/Filament caches and reloads the panel services.
#
# Idempotent — safe to re-run after `jabali update`.
#
# Provider registration: handled by bootstrap/providers.php (file_exists
# guard), not by sed-patching this script. That flipped during the monorepo
# merge (2026-04-13) — the old `];`-anchored sed silently failed against
# the new `], $addonProviders))` close and shipped a half-installed panel.

JABALI_PATH="${JABALI_PATH:-/var/www/jabali}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PANEL_DIR="${SCRIPT_DIR}/panel"

if [[ ! -f "$JABALI_PATH/artisan" ]]; then
    echo "Error: Jabali installation not found at $JABALI_PATH" >&2
    echo "Set JABALI_PATH to your Jabali installation directory." >&2
    exit 1
fi

if [[ ! -d "$PANEL_DIR" ]]; then
    echo "Error: Panel addon files not found at $PANEL_DIR" >&2
    exit 1
fi

echo "Installing jabali-backup panel addon..."
echo "  Jabali path: $JABALI_PATH"
echo ""

# -----------------------------------------------------------------------------
# 1. Agent route
# -----------------------------------------------------------------------------
echo "  [1/6] Agent routes"
install -d -m 0755 /etc/jabali/agent.d
install -m 0644 "$PANEL_DIR/agent/jabali-backup.php" \
    /etc/jabali/agent.d/jabali-backup.php

# -----------------------------------------------------------------------------
# 2. App\Backup namespace (Adapters + Concerns + ServiceProvider)
#    Copy the whole tree so new traits/adapters added later land automatically
#    — the 2026-04-13 breakage was caused by cherry-picking individual files
#    and forgetting BrowsesSnapshots.php.
# -----------------------------------------------------------------------------
echo "  [2/6] App\\Backup namespace"
install -d -m 0755 "$JABALI_PATH/app/Backup"
cp -a "$PANEL_DIR/backup"/. "$JABALI_PATH/app/Backup/"

# -----------------------------------------------------------------------------
# 3. Filament pages
# -----------------------------------------------------------------------------
echo "  [3/6] Filament pages"
install -d -m 0755 \
    "$JABALI_PATH/app/Filament/Admin/Pages" \
    "$JABALI_PATH/app/Filament/Jabali/Pages"
install -m 0644 "$PANEL_DIR/filament/pages/Backups.php" \
    "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php"
install -m 0644 "$PANEL_DIR/filament/pages/SnapshotBrowser.php" \
    "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php"
install -m 0644 "$PANEL_DIR/filament/pages/UserBackups.php" \
    "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php"

# -----------------------------------------------------------------------------
# 4. Blade views (admin pages + partials + components + user page)
# -----------------------------------------------------------------------------
echo "  [4/6] Blade views"
ADMIN_VIEWS="$JABALI_PATH/resources/views/filament/admin/pages"
USER_VIEWS="$JABALI_PATH/resources/views/filament/jabali/pages"
install -d -m 0755 \
    "$ADMIN_VIEWS/partials" \
    "$ADMIN_VIEWS/components" \
    "$USER_VIEWS"

install -m 0644 "$PANEL_DIR/views/backups.blade.php"          "$ADMIN_VIEWS/backups.blade.php"
install -m 0644 "$PANEL_DIR/views/snapshot-browser.blade.php" "$ADMIN_VIEWS/snapshot-browser.blade.php"
install -m 0644 "$PANEL_DIR/views/components/snapshot-file-tree.blade.php" \
    "$ADMIN_VIEWS/components/snapshot-file-tree.blade.php"
for partial in "$PANEL_DIR/views/partials"/*.blade.php; do
    [[ -f "$partial" ]] || continue
    install -m 0644 "$partial" "$ADMIN_VIEWS/partials/$(basename "$partial")"
done
install -m 0644 "$PANEL_DIR/views/user/backups.blade.php" \
    "$USER_VIEWS/backups.blade.php"

# -----------------------------------------------------------------------------
# 5. Public download endpoint
# -----------------------------------------------------------------------------
echo "  [5/6] Download endpoint"
install -m 0644 "$PANEL_DIR/public/backup-download.php" \
    "$JABALI_PATH/public/backup-download.php"

# -----------------------------------------------------------------------------
# 6. Ownership + cache warmup + service reload
# -----------------------------------------------------------------------------
echo "  [6/6] Ownership, caches, services"
chown -R www-data:www-data \
    "$JABALI_PATH/app/Backup" \
    "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php" \
    "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php" \
    "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php" \
    "$ADMIN_VIEWS" \
    "$USER_VIEWS" \
    "$JABALI_PATH/public/backup-download.php" 2>/dev/null || true

(cd "$JABALI_PATH" && sudo -u www-data php artisan view:clear 2>/dev/null) || true
(cd "$JABALI_PATH" && sudo -u www-data php artisan filament:cache-components 2>/dev/null) || true

systemctl restart jabali-agent 2>/dev/null || true
# Best-effort FPM reload across common major versions.
for ver in 8.5 8.4 8.3; do
    systemctl reload "php${ver}-fpm" 2>/dev/null && break || true
done

echo ""
echo "Panel addon installed."
echo ""
echo "  Admin: <panel>/jabali-admin/backups"
echo "  User:  <panel>/jabali-panel/user-backups"
echo ""
echo "To uninstall: ./uninstall-panel.sh"
