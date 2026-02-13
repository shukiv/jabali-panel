# Modern Hosting Control Panel Blueprint

This blueprint describes a modern web hosting control panel (cPanel/DirectAdmin-style) as an architecture + feature map that can be turned into epics and tickets.

## 1) Core goals

- Multi-tenant isolation: customers cannot see or affect each other (files, PHP, mail, DB).
- Automation-first: every UI action is a reproducible, idempotent job.
- Safe-by-default security: least privilege, audit trail, secrets handling, sane defaults.
- Resumable operations: long tasks support retry/continue with logs.
- Scalable topology: start single-node, evolve to web/mail/dns/db/storage nodes.
- Observable system: health checks, metrics, logs, and per-action troubleshooting.

## 2) Reference architecture

### Control plane (panel)

- UI + API, RBAC, tenant/package/quota management
- UI stack: Tailwind CSS + Filament components for panels, forms, tables, and widgets
- Job runner + queue workers
- Audit log + job logs/artifacts
- Central configuration + templates
- Secrets manager

### Data plane (agents on nodes)

- Agent performs privileged ops locally: configs, reloads, users, backups, certbot, etc.
- Panel <-> agent over mTLS (preferred) or HMAC-signed requests.

## 3) Service stack (Nginx + Mail)

### Web node

- Nginx + PHP-FPM (pool per tenant recommended)
- Optional: Redis, WAF (ModSecurity)
- Log rotation

### Mail node

- Postfix (SMTP), Dovecot (IMAP/POP3/LMTP)
- Rspamd (+ Redis) for spam filtering
- DKIM signing (Rspamd or OpenDKIM), SPF/DMARC tooling
- Optional: Sieve filters, Roundcube webmail links

### DNS node

- PowerDNS (API) or BIND9 (template-driven), optional DNSSEC

### DB + backups

- MariaDB/MySQL (local or centralized)
- Backups to local + S3-compatible storage (recommended)

## 4) Tenancy & isolation model

- Linux user per tenant: /home/<tenant>/domains/<domain>/public_html
- PHP-FPM pool per tenant with limits (memory, children, timeouts)
- Strict file permissions; avoid broad www-data write access
- Mail uses virtual mailboxes with quotas and strict ownership

## 5) Feature modules (cPanel/DirectAdmin equivalents)

- Accounts/Packages/Reseller: limits, suspend/unsuspend, usage reporting
- Web hosting: domains/subdomains/redirects, vhost templates, logs viewer
- PHP management: versions, ini overrides, FPM tuning
- SSL: Let's Encrypt, renewals, force HTTPS, HSTS
- DNS: zones + record editor; templates; DKIM/SPF/DMARC assistants
- Databases: DB/user/grants, import/export, remote access allowlist
- Mail: domains, mailboxes, aliases, forwarders, autoresponder, catch-all, spam controls, mail logs/trace
- Files & access: file manager, SFTP/SSH keys, jailed shell optional, cron
- Backups: schedules, targets, restore self-service
- Security: 2FA, IP allowlists, fail2ban hooks, WAF toggle, full audit log

## 6) Automation engine (jobs -> steps)

- Every action runs as a resumable state machine
- Store per-step: status, logs, retries, artifacts (generated configs, dumps)
- Idempotent steps + safe rollback where possible

## 7) Recommended MVP order (Nginx + Mail)

1) Tenants + domains + Nginx vhosts + SSL
2) PHP-FPM pools + limits + logs viewer
3) DNS + DKIM/SPF/DMARC helpers
4) Mail (Postfix/Dovecot) + mailboxes/aliases/forwards + basic logs
5) Rspamd + panel controls
6) Backups (files+DB+mail) + restore
7) Packages/resellers/quotas + suspension logic
8) Hardening + monitoring dashboards

## 8) Epics -> ticket slices (example)

### Job engine

- Job table + step table + artifact store
- State machine + retries
- Agent RPC + ack + log streaming

### Tenants

- Tenant create/suspend workflow
- User isolation + home dir layout
- Package limits + enforcement

### Domains

- Vhost template + apply
- DNS zone creation + records
- SSL issuance + renewal job

### Mail

- Domain enable + DKIM/SPF/DMARC
- Mailbox CRUD + quota
- Forwarders + autoresponder
- Mail logs viewer

### Backups

- Local + S3 target
- Schedule + retention
- Restore job with step logs

## 9) Demo mode considerations

- Read-only middleware should block data mutations but allow authentication.
- Demo deployments may run without privileged agent sockets; provide static demo data for agent-dependent pages.
- Reverse proxy must be trusted to ensure Livewire update URLs are HTTPS.
## Documentation Coverage Notes

- Documentation screenshots are generated for every admin and user page, including tabs where available.
- cPanel Migration tabs (Domains, Databases, Mailboxes, Forwarders, SSL) appear only after a backup is analyzed. Without a sample backup, those tab screenshots cannot be captured.
- To complete coverage, provide a cPanel backup archive and run the screenshot capture flow after analysis.
