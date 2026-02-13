---
title: Quickstart Checklist
---

This checklist helps you go from a fresh server to a verified, working panel.

## Pre-flight

- Fresh Debian 12 or 13 (no existing web/mail stack).
- DNS points the panel host to the server IP.
- PTR (reverse DNS) set for the mail hostname.
- Ports open: 22, 80, 443, 25, 465, 587, 993, 995, 53.

## Install

```
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
```

Optional flags:

- `JABALI_MINIMAL=1` for core-only install
- `JABALI_FULL=1` to force all optional components

## First login

- Admin panel: `https://your-host/jabali-admin`
- User panel: `https://your-host/jabali-panel`
- Webmail: `https://your-host/webmail`

## Post-install verification

1) Check services:

```
systemctl status jabali-agent jabali-queue jabali-health-monitor
```

2) Check SSL:

- Open the admin panel URL and confirm HTTPS is valid.

3) Check mail flow:

- Create a test mailbox and send/receive via SMTP/IMAP.

4) Check DNS:

- If hosting DNS, confirm zone creation and record updates.

## Common fixes

- If the panel does not load, check nginx and ports 80/443.
- If background jobs stall, check `jabali-queue` service status.
- If agent actions fail, check `jabali-agent` service status.
