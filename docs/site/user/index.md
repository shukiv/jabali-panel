# User Docs Index

This is the table of contents for the tenant-facing documentation.

## Getting started

- [Login](./login.md) — the login flow.
- [Two-Factor Challenge](./two-factor-challenge.md) — using TOTP.
- [Request Password Reset](./password-reset-request.md) and [Reset Password](./password-reset-confirm.md).
- [Dashboard](./dashboard.md) — the page you land on after login.
- [Profile](./profile.md) — name, password, 2FA, notifications, backup, usage.

## Domains and DNS

- [Domains](./domains.md) — your hosted domains.
- [DNS Records](./dns-records.md) — records inside a zone.
- [DNSSEC](./dnssec.md) — per-domain signing.
- [SSL](./ssl.md) — TLS certificates.

## Mail

- [Email](./email.md) — parent page; tabs below.
- [Mailboxes](./mailboxes.md).
- [Forwarders](./forwarders.md).
- [Autoresponders](./autoresponders.md).
- [Catch-all](./catch-all.md).
- [Disclaimer](./disclaimer.md).
- [Shared Folders](./shared-folders.md).
- [Email Logs](./email-logs.md).

## Data

- [Databases](./databases.md).
- [Database Users](./db-users.md).
- [PostgreSQL](./postgresql.md).

## Files and code

- [Files](./files.md) — in-panel file manager.
- [SSH Keys](./ssh-keys.md) — manage SFTP keys.
- [PHP Settings](./php-settings.md).
- [Cron Jobs](./cron-jobs.md).
- [Applications](./applications.md) and [WordPress](./wordpress.md).

## Operations

- [Backups](./backups.md) and [Backup Download](./backup-download.md).
- [Logs](./logs.md).
- [Activity](./activity.md) — your account's audit trail.

## Migration

- [cPanel migration](./cpanel-migration.md).
- [DirectAdmin migration](./directadmin-migration.md).

## What to ask the administrator for

A few things only the administrator can do for you:

- Raise your package limits (disk quota, mailbox count, request rate).
- Add a PHP version not yet installed on the host.
- Enable per-app installs for an app not in your current package's allowed list.
- Add a command to the [Cron allowlist](./cron-jobs.md).
- Restore from a backup (when tenant-initiated restore is not enabled for your package).
- Reset your two-factor authentication when you have lost both the authenticator and recovery codes.
