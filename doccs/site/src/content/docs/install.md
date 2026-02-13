---
title: Installation
---

This page covers installation, upgrades, and uninstallation for Jabali Panel.

## System requirements

- Fresh Debian 12 or 13 install (no pre-existing web or mail stack)
- Root access on the server
- A domain for the panel and mail (with glue records if you host DNS)
- PTR (reverse DNS) for the mail hostname
- Open ports: 22, 80, 443, 25, 465, 587, 993, 995, 53

## Quick install (recommended)

Run the installer from GitHub:

```
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
```

Optional flags:

- `JABALI_MINIMAL=1` for core-only install
- `JABALI_FULL=1` to force all optional components

The installer clones the panel to `/var/www/jabali` and configures services,
nginx, SSL, and required system packages.

## Manual install via Debian packages

Jabali ships as two Debian packages:

- `jabali-deps` for system dependencies (nginx, PHP, DB, mail, DNS, etc.)
- `jabali-panel` for the panel application and systemd services

Build the packages from the repository root:

```
./scripts/build-jabali-deps-deb.sh
./scripts/build-jabali-panel-deb.sh
```

Install on the server:

```
sudo dpkg -i ./jabali-deps_<version>_all.deb
sudo apt-get -f install -y
sudo dpkg -i ./jabali-panel_<version>_all.deb
```

After install, systemd services are enabled and started:

- `jabali-agent`
- `jabali-queue`
- `jabali-health-monitor`

## Post-install URLs

- Admin panel: `https://your-host/jabali-admin`
- User panel: `https://your-host/jabali-panel`
- Webmail: `https://your-host/webmail`

## Upgrades

From the panel directory:

```
cd /var/www/jabali
php artisan jabali:upgrade
```

Check for updates only:

```
php artisan jabali:upgrade --check
```

## Uninstall

Before uninstalling, take backups of panel data and user content.

Stop and disable services:

```
sudo systemctl stop jabali-agent jabali-queue jabali-health-monitor
sudo systemctl disable jabali-agent jabali-queue jabali-health-monitor
```

Remove packages (keep configs) or purge (remove configs):

```
sudo apt remove jabali-panel jabali-deps
# or
sudo apt purge jabali-panel jabali-deps
```

Optional cleanup (removes panel files and configs):

```
sudo rm -rf /var/www/jabali
sudo rm -rf /etc/jabali
sudo rm -rf /etc/nginx/jabali
sudo rm -rf /etc/ssl/jabali
sudo rm -f /root/.jabali_db_credentials
sudo rm -f /root/jabali_credentials.txt
```

If you want to remove the panel database and user:

```
sudo mysql -e "DROP DATABASE IF EXISTS jabali;"
sudo mysql -e "DROP USER IF EXISTS jabali@localhost;"
```

## Troubleshooting

- If the panel does not load, confirm nginx is running and ports 80/443 are open.
- Check service status with `systemctl status jabali-agent jabali-queue jabali-health-monitor`.
- Review logs in `storage/logs` and system journal output.
