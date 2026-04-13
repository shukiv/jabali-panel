#!/usr/bin/env bash
set -euo pipefail

echo ""
echo "Uninstalling jabali-backup CLI..."
echo ""

if [[ $EUID -ne 0 ]]; then
    echo "  [x] This must be run as root (sudo ./uninstall.sh)" >&2
    exit 1
fi

# Stop timer if running
if systemctl is-active --quiet jabali-backup.timer 2>/dev/null; then
    systemctl stop jabali-backup.timer
    echo "  [+] Stopped jabali-backup.timer"
fi
systemctl disable jabali-backup.timer 2>/dev/null && echo "  [+] Disabled jabali-backup.timer" || true

# Remove systemd units
for f in /etc/systemd/system/jabali-backup.service /etc/systemd/system/jabali-backup.timer; do
    if [[ -f "$f" ]]; then
        rm "$f"
        echo "  [+] Removed $f"
    fi
done
systemctl daemon-reload 2>/dev/null || true

# Remove binary
if [[ -f /usr/local/bin/jabali-backup ]]; then
    rm /usr/local/bin/jabali-backup
    echo "  [+] Removed /usr/local/bin/jabali-backup"
fi

# Remove libraries
if [[ -d /usr/local/lib/jabali-backup ]]; then
    rm -rf /usr/local/lib/jabali-backup
    echo "  [+] Removed /usr/local/lib/jabali-backup/"
fi

# Remove bash completions
if [[ -f /etc/bash_completion.d/jabali-backup ]]; then
    rm /etc/bash_completion.d/jabali-backup
    echo "  [+] Removed /etc/bash_completion.d/jabali-backup"
fi

# Keep config and secrets (user data)
echo ""
echo "  Kept (remove manually if desired):"
echo "    /etc/jabali-backup/          (config & secrets)"
echo "    /var/log/jabali-backup.log   (log file)"
echo "    /var/cache/jabali-backup/    (restic cache)"
echo ""
echo "To also remove the panel addon: sudo ./uninstall-panel.sh"
echo ""
echo "Done."
