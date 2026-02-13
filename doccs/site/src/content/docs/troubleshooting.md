---
title: Troubleshooting
---

Common issues and where to look first.

## Panel does not load

- Check nginx: `systemctl status nginx`
- Confirm ports 80/443 are open.
- Verify DNS points to the server IP.

## Agent actions fail

- Check agent service:

```
systemctl status jabali-agent
journalctl -u jabali-agent -n 200 --no-pager
```

## Background jobs not running

- Check queue service:

```
systemctl status jabali-queue
journalctl -u jabali-queue -n 200 --no-pager
```

## SSL issuance issues

- Confirm DNS for the domain resolves to the server.
- Check nginx logs: `/var/log/nginx/error.log`

## Mail delivery problems

- Verify PTR and DNS records (SPF, DKIM, DMARC).
- Review mail logs: `/var/log/mail.log` (path may vary by distro).

## Logs

- Panel logs: `/var/www/jabali/storage/logs`
- Nginx logs: `/var/log/nginx`
- Systemd logs: `journalctl -u <service>`
