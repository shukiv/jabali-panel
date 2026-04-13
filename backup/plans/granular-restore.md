# Blueprint: Granular Restore

Implement jabali-backup restore with per-component account restoration from restic snapshots,
conflict handling, dry-run, and target-directory support.

## Objective

Replace the stub in bin/jabali-backup:531 with a full cmd_restore that can restore any
combination of backup components (files, mysql, dns, email, ssl, nginx, php, wordpress,
cron, metadata) from a restic snapshot, either to the live system or to an alternate
directory for inspection.

## Current State

- Backup works: All 11 collectors produce data in /tmp/jabali-backup/{user}/
- restic_restore() exists in lib/restic.sh:79
- Stub: bin/jabali-backup:531 prints "not yet implemented"
- Tested on: 10.0.3.13 with accounts admin, shuki, ssshuki

### Snapshot Layout (from live testing)

```
snapshot 4011a13f
  /home/shuki/                              restic native (files collector)
  /tmp/jabali-backup/shuki/                 staged data
    dns/{domain}.json                       JSON array of records
    dns/{domain}.zone                       BIND zone file
    email/domains/{domain}.json             domain config + DKIM
    email/mailboxes/{email}.json            mailbox config
    email/forwarders.json                   forwarder list
    email/autoresponders.json               autoresponder list
    email/shares.json                       mailbox ACLs
    metadata/account.json                   user record, domains, packages
    mysql/{db}.sql.gz                       database dump
    mysql/grants.sql                        MySQL grants
    nginx/{domain}.conf                     vhost config
    nginx/{domain}.hotlink.json             hotlink rules
    php/{user}.conf                         FPM pool config
    php/version                             PHP version
    ssl/{domain}/{service}/cert.pem         SSL certificate
    ssl/{domain}/{service}/privkey.pem      private key (decrypted)
    ssl/{domain}/{service}/ca-bundle.pem    CA bundle
    ssl/{domain}/{service}/meta.json        cert metadata
    wordpress/{site}/plugins.json           WP plugins
    wordpress/{site}/themes.json            WP themes
    wordpress/{site}/version                WP version
    wordpress/{site}/options.json           WP options
    cron/jobs.json                          cron job list
    cron/crontab                            system crontab
```

## Architecture

### New Files

```
lib/restorers/
  files.sh        Restore /home/{user}/ from snapshot
  mysql.sh        Restore databases + grants
  postgres.sh     Restore PG databases + roles
  dns.sh          Re-import DNS records to Jabali DB
  email.sh        Restore mailbox config + mail storage
  ssl.sh          Re-import SSL certs to DB
  nginx.sh        Copy nginx configs back
  php.sh          Copy PHP-FPM pool configs back
  cron.sh         Re-import cron jobs to DB + system crontab
  metadata.sh     Restore domains, aliases, redirects, webhooks to DB
lib/jabali-encrypt.php  Re-encrypt values for DB import
```

### Restore Flow

```
jabali-backup restore <user> --snapshot=<id>

  1. Extract snapshot to temp dir: /tmp/jabali-restore/{user}/
  2. Read account.json to get user_id, username, domains
  3. For each enabled component (respecting --only/--exclude):
     Run restorer in dependency order:
       a. metadata (user + domains first for FK dependencies)
       b. dns
       c. ssl
       d. email (domains, mailboxes, forwarders, autoresponders)
       e. nginx
       f. php
       g. mysql
       h. postgres
       i. cron
       j. files (last, largest, depends on user existing)
  4. Reload services (nginx, php-fpm) if configs were restored
  5. Print summary
```

### --target Mode

When --target=/path/ is given:
- Extract snapshot to target dir
- Do NOT import anything to DB or copy configs to system
- Just show what was extracted for inspection
- User can manually review before running a real restore

## Steps

### Step 0: Restore Infrastructure

Depends on: nothing. Parallel with: nothing.

Context: Create the cmd_restore function in bin/jabali-backup, the restorer directory,
and the jabali-encrypt.php helper. Wire up option parsing and the extraction pipeline.

Tasks:
1. Create lib/restorers/ directory
2. Create lib/jabali-encrypt.php (counterpart to jabali-decrypt.php)
3. In bin/jabali-backup, replace restore stub with cmd_restore that:
   - Parses --snapshot, --only, --exclude, --target, --force, --dry-run
   - Finds the snapshot (resolve "latest" to actual ID for the user)
   - Extracts to /tmp/jabali-restore/{user}/ via restic_restore()
   - Sources and runs restorers in dependency order
   - Prints summary
4. Add helper: _db_write() to run INSERT/UPDATE SQL against Jabali DB

Verification:
```bash
jabali-backup restore shuki --snapshot=latest --dry-run
jabali-backup restore shuki --snapshot=latest --target=/tmp/inspect/
```

Exit criteria: --dry-run lists components found in snapshot. --target extracts to directory.

### Step 1: Files Restorer

Depends on: Step 0. Parallel with: Steps 2-8.

Tasks:
1. Create lib/restorers/files.sh:
   - restore_files(username, extract_dir, home_dir, force):
     - Source is {extract_dir}/home/{username}/
     - If --target: already extracted, done
     - If home_dir exists and not --force: warn and skip
     - If --force or home_dir empty: rsync -a from extracted to live home
     - Fix ownership: chown -R {username}:{username} {home_dir}

### Step 2: MySQL Restorer

Depends on: Step 0. Parallel with: Steps 1, 3-8.

Tasks:
1. Create lib/restorers/mysql.sh:
   - restore_mysql(username, extract_dir, force):
     - Read staging: {extract_dir}/tmp/jabali-backup/{username}/mysql/
     - For each {db}.sql.gz:
       - Check if DB exists
       - If exists + no --force: create as {db}_restored
       - If exists + --force: DROP DATABASE then recreate
       - gunzip -c {db}.sql.gz | mysql
     - Replay grants from grants.sql
     - Re-create MySQL credential in Jabali DB

### Step 3: DNS Restorer

Depends on: Step 0 (and metadata restorer for domain IDs). Parallel with: Steps 1, 2, 4-8.

Tasks:
1. Create lib/restorers/dns.sh:
   - restore_dns(user_id, extract_dir, force):
     - Read {domain}.json files
     - For each domain: look up domain_id from domains table
     - If records exist + no --force: merge (add missing, skip duplicates by name+type)
     - If --force: DELETE existing then insert all
     - INSERT INTO dns_records (domain_id, name, type, content, ttl, priority)

### Step 4: Email Restorer

Depends on: Step 0. Parallel with: Steps 1-3, 5-8.

Tasks:
1. Create lib/restorers/email.sh:
   - restore_email(user_id, username, extract_dir, force):
     - Email domains: Read email/domains/{domain}.json
       - Insert/update email_domains record
       - Re-encrypt DKIM key via jabali-encrypt.php if .dkim.key exists
     - Mailboxes: Read email/mailboxes/{email}.json
       - Insert mailbox record (password left NULL, user must reset)
       - Copy mail storage to Stalwart data dir if exists
       - Fix ownership: chown -R {system_uid}:{system_gid}
     - Forwarders: Read email/forwarders.json, insert to email_forwarders
     - Autoresponders: Read email/autoresponders.json, insert to autoresponders
     - Shares: Read email/shares.json, insert to mailbox_shares

### Step 5: SSL Restorer

Depends on: Step 0. Parallel with: Steps 1-4, 6-8.

Tasks:
1. Create lib/restorers/ssl.sh:
   - restore_ssl(user_id, extract_dir, force):
     - Read ssl/{domain}/{service}/ directories
     - Read cert.pem, ca-bundle.pem, privkey.pem, meta.json
     - Re-encrypt private key via jabali-encrypt.php
     - Insert or update ssl_certificates record

### Step 6: Nginx and PHP Restorer

Depends on: Step 0. Parallel with: Steps 1-5, 7-8.

Tasks:
1. Create lib/restorers/nginx.sh:
   - Copy nginx/{domain}.conf to /etc/nginx/sites-enabled/
   - Restore hotlink settings from .hotlink.json to domain_hotlink_settings table
   - nginx -t and nginx -s reload
2. Create lib/restorers/php.sh:
   - Copy php/{user}.conf to pool dir, read php/version
   - systemctl reload php{version}-fpm

### Step 7: Cron Restorer

Depends on: Step 0. Parallel with: Steps 1-6, 8.

Tasks:
1. Create lib/restorers/cron.sh:
   - Read cron/jobs.json, insert to cron_jobs table
   - Read cron/crontab, apply via crontab -u {username}

### Step 8: Metadata Restorer

Depends on: Step 0. Parallel with: Steps 1-7 (but runs FIRST in execution order).

Context: Restore the core account record. This MUST run before DNS, email, SSL restorers
because they need domain_id foreign keys.

Tasks:
1. Create lib/restorers/metadata.sh:
   - restore_metadata(extract_dir, force):
     - Read metadata/account.json
     - User: Check if exists by username
       - If exists + no --force: use existing user_id, skip update
       - If exists + --force: update record (preserve id + password)
       - If not exists: INSERT into users (password NULL, admin must reset)
     - Domains: Insert or update domains records
     - Domain aliases: Insert to domain_aliases
     - Domain redirects: Insert to domain_redirects
     - Git deployments: Insert (re-encrypt secret tokens)
     - Webhooks: Insert (re-encrypt secret tokens)
     - Return user_id for downstream restorers

### Step 9: Integration Testing on 10.0.3.13

Depends on: Steps 0-8. Parallel with: nothing.

Tasks:
1. Backup shuki account
2. Delete some data (DNS records, rename a DB)
3. Restore with --force and verify everything is back
4. Test --target mode (extract without applying)
5. Test --only=mysql,dns selective restore
6. Test --dry-run output
7. Test conflict handling without --force

Verification:
```bash
jabali-backup run shuki
jabali-backup restore shuki --snapshot=latest --target=/tmp/inspect/
jabali-backup restore shuki --snapshot=latest --force
jabali-backup restore shuki --snapshot=latest --only=dns --dry-run
```

Exit criteria: Full restore works. Selective restore works.
Dry-run output is accurate. --target extracts without modifying live system.

## Dependency Graph

```
Step 0 (infrastructure)
  Step 1 (files)
  Step 2 (mysql)
  Step 3 (dns)
  Step 4 (email)       All parallel (independent restorers)
  Step 5 (ssl)
  Step 6 (nginx+php)
  Step 7 (cron)
  Step 8 (metadata)
                |
         Step 9 (integration test)
```

Note: Steps 1-8 are coded in parallel but EXECUTED in dependency order at runtime:
metadata, dns, ssl, email, nginx, php, mysql, postgres, cron, files

## Invariants

1. --target mode NEVER writes to live system (DB, filesystem, services)
2. --dry-run NEVER writes anything, only prints plan
3. Without --force, existing data is NEVER overwritten
4. Restore extraction uses trap EXIT cleanup, temp dir always removed
5. All DB writes use parameterized values (no SQL injection via restored data)
6. Private keys re-encrypted before DB insert, never stored plaintext in DB
7. Services only reloaded if their configs were actually changed
