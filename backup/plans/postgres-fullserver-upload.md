# Blueprint: PostgreSQL Restore + Full-Server Backup + Upload Restore

**Objective:** Three enhancements:
1. PostgreSQL databases are collected but never restored — add the restorer
2. Panel lacks a "backup all users" option — users must select accounts one by one
3. No way to upload a backup archive and restore from it via the panel

**Date:** 2026-04-10
**Branch:** feat/postgres-fullserver-upload
**Base:** main

---

## Current State Analysis

### PostgreSQL
- `lib/collectors/postgres.sh` — EXISTS, called at line 572 of `bin/jabali-backup`
- `lib/restorers/postgres.sh` — **MISSING** (no file exists)
- `cmd_restore()` — has 11 restore steps but NO postgres step between MySQL (#9) and Cron (#10)
- Panel `jbRun()` component map: `include_databases => mysql` (postgres not independently toggleable)
- `jbValidateComponentList()` and `jbRestore()` DO include `postgres` in valid lists

### Full Server Backup
- CLI `jabali-backup run` with no args already backs up ALL accounts via `discover_accounts()`
- Panel `jbRun()` supports empty username (= all accounts)
- Panel `createBackup` action **requires** a username (`->required()`) — no "all users" option
- Need: remove required constraint, add "All Accounts" option to dropdown

### Upload Restore
- Download works via named pipe streaming (`jbDownloadPipe`)
- No upload route, no CLI import-from-archive command, no panel UI for upload
- Need: `jabali-backup import` CLI command + `jb.upload_restore` agent route + panel action

---

## Steps

### Step 1: PostgreSQL Restorer
**Files:** `lib/restorers/postgres.sh`, `bin/jabali-backup`
**Depends on:** nothing
**Model tier:** default
**Rollback:** `git revert`

**Context:**
The MySQL restorer (`lib/restorers/mysql.sh`) is the closest reference. The postgres collector saves:
- `{staging}/postgres/role.sql` — role definition from `pg_dumpall --roles-only`
- `{staging}/postgres/{dbname}.dump` — custom-format dumps from `pg_dump -Fc`

The restorer must:
1. Recreate the role from `role.sql` (idempotent — skip if exists)
2. For each `.dump` file, create the database if missing and `pg_restore -d` into it
3. If `--force`, drop and recreate existing databases
4. If not forced, restore to `{dbname}_restored` when database exists
5. Grant ownership to the user's role

**Tasks:**
- [ ] Create `lib/restorers/postgres.sh` with `restore_postgres()` function
- [ ] Add postgres restore step to `cmd_restore()` between MySQL (#9) and Cron (#10)
- [ ] Add postgres to `--target` extraction summary in `cmd_restore()` (line ~806)
- [ ] Add postgres to dry-run restore plan output

**Verification:**
```bash
# Check file exists and function defined
grep -q 'restore_postgres' lib/restorers/postgres.sh
# Check it's called in restore chain
grep -q 'restore_postgres' bin/jabali-backup
# Syntax check
bash -n lib/restorers/postgres.sh
bash -n bin/jabali-backup
```

**Exit criteria:** `jabali-backup restore <user> --only=postgres --dry-run` shows postgres in plan

---

### Step 2: Full Server Backup (Panel + CLI)
**Files:** `panel/filament/pages/Backups.php`, `panel/agent/jabali-backup.php`, `panel/views/backups.blade.php`
**Depends on:** nothing (parallel with Step 1)
**Model tier:** default
**Rollback:** `git revert`

**Context:**
The CLI already supports `jabali-backup run` (all accounts). The panel `createBackup` action at line 127 requires a single username. The `jbRun()` function at line 381 already handles empty username = all accounts.

Changes needed:
1. Make username **optional** in createBackup form — add a "All Accounts" placeholder option
2. When no username selected, pass empty username to `jb.run` (triggers all-accounts backup)
3. Update notification message to say "all accounts" when no specific user
4. The `include_databases` toggle should note that it covers both MySQL and PostgreSQL

**Tasks:**
- [ ] In `Backups.php` `createBackup` action: make username not required, add placeholder "All Accounts"
- [ ] Update action handler to support empty username → all accounts
- [ ] Update notification message for all-accounts case
- [ ] Add `include_postgres` toggle alongside databases (or label "MySQL + PostgreSQL")

**Verification:**
```bash
php -l panel/filament/pages/Backups.php
```

**Exit criteria:** Panel shows "All Accounts" option in backup dropdown; triggering it starts backup for all users

---

### Step 3: Upload Restore — CLI `import` command
**Files:** `bin/jabali-backup`
**Depends on:** nothing (parallel with Steps 1-2)
**Model tier:** default
**Rollback:** `git revert`

**Context:**
The download command creates a tar.gz archive with structure:
```
{username}/home/...
{username}/mysql/...
{username}/dns/...
{username}/postgres/...
...
```

The import command needs to:
1. Accept a tar.gz file path
2. Extract to temp directory
3. Discover usernames from top-level directories
4. For each user, call the existing restore flow (or offer component selection)
5. Clean up temp directory

This reuses the existing `cmd_restore()` logic but feeds it an extracted archive instead of a restic snapshot.

**Tasks:**
- [ ] Add `import` to usage text and command dispatch
- [ ] Implement `cmd_import()` that:
  - Validates archive exists and is readable
  - Extracts to temp dir
  - Lists discovered users/components
  - Supports `--dry-run` to preview
  - Supports `--only=` component filter
  - Supports `--force` flag
  - Calls individual restorers per discovered user
- [ ] Add `import` to bash completions in `install.sh`

**Verification:**
```bash
bash -n bin/jabali-backup
jabali-backup import --help 2>&1 | grep -q 'import'
```

**Exit criteria:** `jabali-backup import /path/to/backup.tar.gz --dry-run` shows discovered users and components

---

### Step 4: Upload Restore — Agent Route
**Files:** `panel/agent/jabali-backup.php`
**Depends on:** Step 3
**Model tier:** default
**Rollback:** `git revert`

**Context:**
The agent receives files uploaded by the panel (Filament handles multipart upload to a temp path). The agent route needs to:
1. Accept uploaded file path + optional parameters
2. Validate the file is a tar.gz
3. Move it to a safe temp location
4. Call `jabali-backup import` with the file
5. Return result (discovered users, restore status)

The existing `jbExec()` and `jbExecBackground()` helpers handle CLI execution.

**Tasks:**
- [ ] Add `jb.upload_restore` route to route table
- [ ] Implement `jbUploadRestore()`:
  - Validate uploaded file exists and is tar.gz (check magic bytes, not just extension)
  - Copy to `/tmp/jabali-import-{token}.tar.gz`
  - Run `jabali-backup import` with the temp file
  - Return structured result
- [ ] Add `jb.upload_preview` route for dry-run preview
- [ ] Implement `jbUploadPreview()`:
  - Extract and list users/components without restoring
  - Return discovery info for the panel to show

**Verification:**
```bash
php -l panel/agent/jabali-backup.php
```

**Exit criteria:** Agent can receive a file path and invoke `jabali-backup import`

---

### Step 5: Upload Restore — Panel UI
**Files:** `panel/filament/pages/Backups.php`, `panel/views/backups.blade.php`
**Depends on:** Step 4
**Model tier:** default
**Rollback:** `git revert`

**Context:**
The Backups page header actions have "Create Backup" and "Refresh". Add an "Import Backup" action that:
1. Shows a file upload field (Filament `FileUpload`)
2. On upload, calls `jb.upload_preview` to discover contents
3. Shows discovered users and components with toggles
4. On confirm, calls `jb.upload_restore` to execute

Filament `FileUpload` stores to disk, provides the path. The action handler reads the path and passes to agent.

**Tasks:**
- [ ] Add `importBackup` header action to `getHeaderActions()`
- [ ] Action form: FileUpload for .tar.gz, max 5GB
- [ ] Two-step: preview first (show users/components), then confirm
- [ ] Add force toggle and component selection after preview
- [ ] Handle success/error notifications
- [ ] Clean up uploaded file after import completes

**Verification:**
```bash
php -l panel/filament/pages/Backups.php
```

**Exit criteria:** Panel shows "Import Backup" button; uploading a tar.gz shows preview, then restores on confirm

---

### Step 6: Install Script + Docs
**Files:** `install.sh`, `docs/README.md`, `docs/RESTORE.md`, `docs/CONFIGURATION.md`
**Depends on:** Steps 1-5
**Model tier:** default
**Rollback:** `git revert`

**Tasks:**
- [ ] Add `postgres.sh` to install copy list in `install.sh` (restorer)
- [ ] Add `import` to bash completion commands
- [ ] Update `docs/README.md` — add PostgreSQL to restore capabilities, mention full-server backup, import command
- [ ] Update `docs/RESTORE.md` — add PostgreSQL restore section, import-from-archive section
- [ ] Update `docs/CONFIGURATION.md` if any new config keys needed

**Verification:**
```bash
grep -q 'restorers/postgres.sh' install.sh
grep -q 'import' install.sh  # bash completions
```

**Exit criteria:** Fresh install picks up postgres restorer; docs reflect all three features

---

## Dependency Graph

```
Step 1 (postgres restorer) ─────────────────────────┐
Step 2 (full-server panel) ─────────────────────────┤
Step 3 (import CLI) ──→ Step 4 (agent route) ──→ Step 5 (panel UI) ──┤
                                                                      ↓
                                                              Step 6 (install + docs)
```

Steps 1, 2, 3 can run in **parallel**.
Steps 4-5 are serial (each depends on the previous).
Step 6 runs last after all features are complete.

## Invariants (verify after every step)

1. `bash -n bin/jabali-backup` passes
2. `php -l panel/agent/jabali-backup.php` passes
3. `php -l panel/filament/pages/Backups.php` passes
4. Existing backup/restore commands still work (no regressions)
5. No hardcoded credentials in any file
