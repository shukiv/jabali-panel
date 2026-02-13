---
title: Operations and Maintenance
---

This page covers routine operations for stable, predictable hosting.

## Backups

- Use the admin backups page to configure schedules and retention.
- Test restore workflows on a staging account before relying on them.
- Keep at least one offsite destination configured.

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

## Service health

Key services:

- `jabali-agent`
- `jabali-queue`
- `jabali-health-monitor`

Check status:

```
systemctl status jabali-agent jabali-queue jabali-health-monitor
```

Restart if needed:

```
sudo systemctl restart jabali-agent jabali-queue jabali-health-monitor
```

## Log locations

- Panel logs: `/var/www/jabali/storage/logs`
- Nginx logs: `/var/log/nginx`
- System services: `journalctl -u <service>`

## Common maintenance tasks

- Rotate or archive old backups to control disk usage.
- Review failed jobs and mail queues regularly.
- Audit admin users and remove unused accounts.
