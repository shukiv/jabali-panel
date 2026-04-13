#!/usr/bin/env bash
set -euo pipefail

JABALI_PATH="${JABALI_PATH:-/var/www/jabali}"

echo "Uninstalling jabali-backup panel addon..."

for f in \
    /etc/jabali/agent.d/jabali-backup.php \
    "$JABALI_PATH/app/Filament/Admin/Pages/Backups.php" \
    "$JABALI_PATH/resources/views/filament/admin/pages/backups.blade.php" \
    "$JABALI_PATH/app/Filament/Jabali/Pages/UserBackups.php" \
    "$JABALI_PATH/resources/views/filament/jabali/pages/backups.blade.php" \
    "$JABALI_PATH/app/Backup/Adapters/ResticSnapshotAdapter.php" \
    "$JABALI_PATH/app/Backup/BackupServiceProvider.php" \
    "$JABALI_PATH/app/Filament/Admin/Pages/SnapshotBrowser.php"
do
    if [[ -f "$f" ]]; then
        rm "$f"
        echo "  Removed $f"
    fi
done

# Remove service provider registration
sed -i '/BackupServiceProvider/d' "$JABALI_PATH/bootstrap/providers.php" 2>/dev/null || true

cd "$JABALI_PATH"
php artisan view:clear 2>/dev/null || true
php artisan filament:cache-components 2>/dev/null || true
systemctl restart jabali-agent 2>/dev/null || true
systemctl reload php8.5-fpm 2>/dev/null || systemctl reload php8.4-fpm 2>/dev/null || systemctl reload php8.3-fpm 2>/dev/null || true

echo "Done."
