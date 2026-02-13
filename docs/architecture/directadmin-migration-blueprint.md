# DirectAdmin Migration Blueprint

This blueprint describes how Jabali Panel should migrate accounts from a remote
DirectAdmin server into Jabali.

It is written to match Jabali's current architecture:
- Laravel 12 + Filament v5 + Livewire v4 UI.
- Privileged agent (`bin/jabali-agent`) for root-level operations.
- Long-running work via jobs/queue with resumable logs.

## Goals

- Support connecting to a remote DirectAdmin server (host, port, credentials).
- Support multi-account migrations (admin-initiated).
- Support user self-migration (user-initiated, scoped to their Jabali account).
- Migrate websites, databases, email, and SSL.
- Provide clear progress, per-account logs, and safe retries.

## Non-Goals (Initial Scope)

- Reseller plans and quota mapping (can be added later).
- DNS zone migrations from DirectAdmin (optional later).
- Password migration for website logins and mailboxes (not possible in general).

## UX Overview

Jabali already has:
- Admin migration entry: `jabali-admin/migration` (tabs page).
- User migration entry: `jabali-panel/cpanel-migration` (cPanel only today).

DirectAdmin migration should be added to both panels:
- Admin: new migration tab alongside cPanel and WHM.
- User: new self-migration page similar to user cPanel migration.

The UI should use Filament native components (Wizard, Sections, Tables), and
should not embed custom HTML/CSS.

## Admin Flow (Multi-Account)

### Step 1: Connect

Inputs:
- Hostname or IP
- Port (default DirectAdmin: 2222)
- Auth:
  - Username + password (initial)
  - Optional future: API token
- SSL verify toggle (default on, allow off for lab servers)

Actions:
- Test connection
- Discover users/accounts

Output:
- Server metadata (DirectAdmin version if available)
- Discovered accounts summary

### Step 2: Select Accounts

Show a table of discovered accounts:
- Source username
- Main domain
- Email contact
- Disk usage (if provided)

Selection:
- Multi-select accounts for import.

Per-account mapping:
- Target Jabali username (editable, default = source username)
- Target user email (editable)
- Conflict indicators (existing Jabali user, existing domains)

### Step 3: Choose What To Import

Toggles:
- Files
- Databases
- Email
- SSL

Optional safety toggles:
- Skip existing domains
- Skip existing databases
- Re-issue SSL via Let's Encrypt when custom SSL is missing or invalid

### Step 4: Run Migration

Execution runs as a background job batch:
- Per-account status: pending, running, completed, failed, skipped.
- Per-account logs (timestamps + messages).
- Global log for the import job.

Controls:
- Cancel import (best-effort stop at safe boundaries).
- Retry failed accounts.

## User Flow (Self-Migration)

User self-migration is a guided flow to import a single DirectAdmin account into
the currently authenticated Jabali user.

### Step 1: Connect

Inputs:
- DirectAdmin hostname/IP and port
- DirectAdmin username + password

Actions:
- Test connection
- Verify the DirectAdmin user is accessible

### Step 2: Choose What To Import

Toggles:
- Files
- Databases
- Email
- SSL

### Step 3: Run Migration

Show:
- Live progress
- Logs
- Final summary

Scope and enforcement:
- Target Jabali user is fixed to the authenticated user.
- Import must refuse to touch domains that do not belong to the user.

## Data Model (Proposed)

Jabali already has:
- `server_imports` and `server_import_accounts`.

To support DirectAdmin remote migration properly, add:
- `server_imports.created_by_user_id` (nullable, for admin-created vs user-created).
- `server_imports.target_user_id` (nullable, for admin selecting a target user, optional).
- `server_import_accounts.backup_path` (nullable, per-account backup archive path when remote).
- `server_import_accounts.ssl_items` (json, optional, discovered SSL material per domain).
- `server_import_accounts.mail_items` (json, optional, discovered mailboxes and domains).

Also update `server_imports.import_options` to include:
- `files`, `databases`, `emails`, `ssl`.

## Import Pipeline (High-Level)

### Phase A: Discovery

Purpose:
- Validate credentials and enumerate accounts to import.

Implementation notes:
- Jabali agent already supports discovery for DirectAdmin remote via:
  - `CMD_API_SHOW_ALL_USERS`
  - `CMD_API_SHOW_USER_CONFIG`

### Phase B: Backup Creation and Download (Remote Method)

Problem:
- The current import processor only imports from local backup archives.

Solution:
- For `import_method=remote_server`, create and download a DirectAdmin backup
  per selected account to `storage/app/private/imports/...`.

Implementation choices:
- Run this phase in a queued job to avoid request timeouts.
- Download must stream to disk, not to memory.
- Store paths per account (`server_import_accounts.backup_path`).

### Phase C: Analyze Backup (Optional But Recommended)

Purpose:
- Extract account metadata (domains, DB dumps, email list, SSL presence).
- Show a preview before the destructive restore phase.

Implementation notes:
- Reuse the existing agent discovery for backup files (`import.discover`).
- Extend discovery to detect:
  - Mailbox domains and mailbox names
  - SSL certificate files per domain (if present)

### Phase D: Restore Into Jabali

For each account:
1. Create or map Jabali user.
2. Create domains.
3. Restore website files into the correct document roots.
4. Restore databases and import dumps.
5. Restore email domains and mailboxes, then copy Maildir data.
6. Restore SSL certificates (or issue Let's Encrypt if configured).

All steps must write logs to both:
- `server_imports.import_log`
- `server_import_accounts.import_log`

## Email Migration (Requirements)

Minimum requirements:
- Create mail domains in Jabali for the migrated domains.
- Create mailboxes for the discovered mailbox usernames.
- Import Maildir content (messages, folders) into the new mailboxes.

Recommended approach:
- Use Jabali agent functions:
  - `email.enable_domain`
  - `email.mailbox_create`
  - `email.sync_maps` and `email.reload_services` when needed

Notes:
- DirectAdmin backups can include hashed mailbox passwords, which are not
  directly reusable for Dovecot. Use new random passwords and provide a
  "reset passwords" output list to the admin or user.

## SSL Migration (Requirements)

Minimum requirements:
- If custom SSL material is present in the DirectAdmin backup, install it for
  each domain in Jabali.

Recommended approach:
- Extract PEM certificate, private key, and optional chain from the backup.
- Use Jabali agent function `ssl.install` per domain.

Fallback:
- If SSL material is missing or invalid, allow issuing Let's Encrypt via
  `ssl.issue` after DNS and vhost are ready.

## Multi-Account Support

Admin migration must allow selecting multiple DirectAdmin users and migrating
them in one batch.

Execution model:
- One `server_imports` record per batch.
- One `server_import_accounts` record per DirectAdmin user.
- Independent status and retry per account.

## Security and Compliance

- Store remote passwords and tokens encrypted at rest (already supported for
  `server_imports.remote_password` and `server_imports.remote_api_token`).
- Never write raw credentials into logs.
- Provide a "forget credentials" action after the migration completes.
- Rate-limit connection tests and discovery to reduce abuse.

## Observability

- Show per-account progress and current step in the UI.
- Write a compact global log and a detailed per-account log.
- On failures, capture enough context to troubleshoot:
  - Which step failed
  - The relevant domain or database name
  - A short error string without secrets

## Implementation Phases (Suggested)

Phase 1:
- Admin: add DirectAdmin migration tab (UI skeleton).
- User: add DirectAdmin self-migration page (UI skeleton).
- Wire UI to the existing `server_imports` discovery (remote and backup-file).

Phase 2:
- Implement remote backup creation and download.
- Store per-account backup paths.
- Make `import:process` support `remote_server` by using downloaded archives.

Phase 3:
- Implement email restore (mail domains, mailboxes, Maildir copy).
- Implement SSL restore (custom cert install, LE fallback).

Phase 4:
- Add tests (discovery, permissions, per-account import state).
- Add docs and screenshots for the new pages/tabs.

