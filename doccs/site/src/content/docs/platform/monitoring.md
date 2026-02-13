---
title: Monitoring and Alerts
description: Health monitoring, logs, and notifications.
sidebar:
  order: 3
---

Jabali includes a health monitor daemon and a notification system to keep administrators informed.

## Health monitor

- Checks critical services every 30 seconds
- Attempts automatic restarts on failures
- Logs events to /var/log/jabali/health-monitor.log

## Alert types

- SSL expiration and errors
- Backup failures
- Disk quota warnings
- Login failures
- Service health alerts
- High load notifications

## Operational tips

- Configure alert recipients during initial setup
- Test the mail sender before relying on alerts
- Review notification logs during incidents
