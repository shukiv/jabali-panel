---
title: Backup System
description: Local and remote backups with restore options.
sidebar:
  order: 4
---

Backups are available for individual users and for the full server. They include files, databases, mail, and configuration data.

## Backup types

- User backups stored under /home/<user>/backups
- Server backups stored under /var/backups/jabali
- Incremental backups to remote targets

## Restore coverage

- Website files
- Databases and users
- Mailboxes
- SSL certificates
- DNS zone files

## Best practices

- Keep at least one remote destination
- Test restores on a schedule
- Maintain retention policies to save storage
