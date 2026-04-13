#!/usr/bin/env bash
set -euo pipefail

# Install jabali-backup panel addon into the Jabali hosting panel.
#
# Installs:
#   1. Agent route file      -> /etc/jabali/agent.d/jabali-backup.php
#   2. Admin Filament page   -> {jabali}/app/Filament/Admin/Pages/Backups.php
#   3. Admin Blade view      -> {jabali}/resources/views/filament/admin/pages/backups.blade.php
#   4. User Filament page    -> {jabali}/app/Filament/Jabali/Pages/UserBackups.php
#   5. User Blade view       -> {jabali}/resources/views/filament/jabali/pages/backups.blade.php
#
# After install, restart jabali-agent and php-fpm.

JABALI_PATH="${JABALI_PATH:-/var/www/jabali}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PANEL_DIR="${SCRIPT_DIR}/panel"

echo "Installing jabali-backup panel addon..."
echo "  Jabali path: $JABALI_PATH"
echo ""

if [[ ! -f "$JABALI_PATH/artisan" ]]; then
    echo "Error: Jabali installation not found at $JABALI_PATH"
    echo "Set JABALI_PATH to your Jabali installation directory."
    exit 1
fi

if [[ ! -d "$PANEL_DIR" ]]; then
    echo "Error: Panel addon files not found at $PANEL_DIR"
    exit 1
fi

# 1. Agent route file
echo "  [1/5] Agent routes"
mkdir -p /etc/jabali/agent.d
cp "$PANEL_DIR/agent/jabali-backup.php" /etc/jabali/agent.d/jabali-backup.php
chmod 644 /etc/jabali/agent.d/jabali-backup.php

# 2. Admin Filament page
echo "  [2/5] Admin page"
mkdir -p "$JABALI_PATH/app/Filament/Admin/Pages"
cp "$PANEL_DIR/filament/pages/Backups.php" "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php"

# 3. Admin Blade views
echo "  [3/5] Admin views"
mkdir -p "$JABALI_PATH/resources/views/filament/admin/pages/partials"
cp "$PANEL_DIR/views/backups.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/backups.blade.php"
cp "$PANEL_DIR/views/partials/snapshot-browser-embed.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/partials/snapshot-browser-embed.blade.php"
cp "$PANEL_DIR/views/partials/restore-file-badges.blade.php" "$JABALI_PATH/resources/views/filament/admin/pages/partials/restore-file-badges.blade.php"

# 4. User Filament page
echo "  [4/9] User page"
mkdir -p "$JABALI_PATH/app/Filament/Jabali/Pages"
cp "$PANEL_DIR/filament/pages/UserBackups.php" "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php"

# 5. User Blade view
echo "  [5/9] User view"
mkdir -p "$JABALI_PATH/resources/views/filament/jabali/pages"
cp "$PANEL_DIR/views/user/backups.blade.php" "$JABALI_PATH/resources/views/filament/jabali/pages/backups.blade.php"

# 6. Backup addon classes (adapter, service provider)
echo "  [6/9] Backup addon classes"
mkdir -p "$JABALI_PATH/app/Backup/Adapters"
cp "$PANEL_DIR/backup/Adapters/ResticSnapshotAdapter.php" "$JABALI_PATH/app/Backup/Adapters/ResticSnapshotAdapter.php"
cp "$PANEL_DIR/backup/BackupServiceProvider.php" "$JABALI_PATH/app/Backup/BackupServiceProvider.php"

# 7. Snapshot browser page
echo "  [7/9] Snapshot browser page"
cp "$PANEL_DIR/filament/pages/SnapshotBrowser.php" "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php"

# 8. Download endpoint
echo "  [8/9] Download endpoint"
cp "$PANEL_DIR/public/backup-download.php" "$JABALI_PATH/public/backup-download.php"

# 9. Register service provider (if not already registered)
echo "  [9/9] Registering service provider"
if ! grep -q 'BackupServiceProvider' "$JABALI_PATH/bootstrap/providers.php" 2>/dev/null; then
    # Add to the providers array
    sed -i '/^];$/i\    App\\Backup\\BackupServiceProvider::class,' "$JABALI_PATH/bootstrap/providers.php"
    echo "    Added BackupServiceProvider to bootstrap/providers.php"
fi

# Clear caches
echo ""
echo "  Clearing caches..."
cd "$JABALI_PATH"
php artisan view:clear 2>/dev/null || true
php artisan filament:cache-components 2>/dev/null || true

# Reload services
echo "  Reloading services..."
systemctl restart jabali-agent 2>/dev/null || true
systemctl reload php8.5-fpm 2>/dev/null || systemctl reload php8.4-fpm 2>/dev/null || systemctl reload php8.3-fpm 2>/dev/null || true

echo ""
echo "Panel addon installed."
echo ""
echo "  Admin:  https://your-server/jabali-admin/backups"
echo "  User:   https://your-server/jabali-panel/user-backups"
echo ""
echo "To uninstall: ./uninstall-panel.sh"
