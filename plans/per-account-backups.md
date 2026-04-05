# Plan: Per-Account Backups

## Objective

Migrate from bulk server backups (one restic snapshot containing all users) to per-account backups (one snapshot per user). Restore and download operate on individual accounts.

## Current State

- "Create Server Backup" calls `backupCreateServer()` which backs up ALL users into a single restic snapshot
- `Backup` model has nullable `user_id` — null = server-wide
- Restore wizard has a user-selection step but browses a monolithic snapshot
- Per-user backups exist (`backupCreate()`) but are separate tar.gz files, not restic snapshots
- Schedules support both server-wide and per-user via `is_server_backup` flag

## Target State

- "Create Server Backup" iterates all active users, creates one restic snapshot per user
- Each `Backup` record has `user_id` set (no more null/server-wide)
- Admin backups page shows backups grouped/filterable by user
- User panel shows only their own backups
- Restore operates on a single user's snapshot directly (no user-selection step needed)
- Download streams a single user's snapshot

## Steps

### Step 1: Agent — Per-User Restic Snapshots (serial, strongest model)

**Context**: The agent function `backupCreateServer()` currently backs up all user paths in one `restic backup` call. We need a new function that creates one snapshot per user.

**Files**:
- `bin/jabali-agent` — `backupCreateServer()` function

**Tasks**:
1. Read the current `backupCreateServer()` implementation
2. Create `backupCreateUserSnapshot(array $params)` that:
   - Takes `username`, `repo`, `destination`, `include_databases`, `include_mail`
   - Dumps databases for that user to `~/.jabali-backup/databases/`
   - Runs `restic backup /home/{username} /var/mail/vhosts/*/username` (scoped paths)
   - Tags the snapshot with `--tag user:{username}`
   - Returns `snapshot_id`, `size_bytes`, `file_count`
3. Register the new route: `'backup.create_user_snapshot' => backupCreateUserSnapshot($params)`
4. Keep `backupCreateServer()` working (deprecated, not removed yet)

**Verification**: `php -l bin/jabali-agent`

**Exit criteria**: Agent has a new RPC that creates a single-user restic snapshot with proper tagging.

---

### Step 2: Orchestrator — Iterate Users for Server Backup (serial, depends on Step 1)

**Context**: `BackupOrchestrator::createServerBackup()` currently creates one Backup record and calls the agent once. It needs to iterate users and create one Backup per user.

**Files**:
- `app/Services/Backup/BackupOrchestrator.php`
- `app/Jobs/RunServerBackup.php`
- `app/Console/Commands/RunBackupSchedules.php`

**Tasks**:
1. Read `BackupOrchestrator::createServerBackup()` fully
2. Refactor to:
   - Get list of active users (from `includes` config or all active users)
   - For each user: create a `Backup` record with `user_id` set, call `backup.create_user_snapshot`
   - Update each record with snapshot_id, size, status
   - Return array of results (not single result)
3. Update `RunServerBackup` job if it wraps the orchestrator
4. Update `RunBackupSchedules` command to handle per-user backup records
5. Retention policy should apply per-user (keep last N snapshots per user, not globally)

**Verification**: `php artisan test --compact --filter=Backup`

**Exit criteria**: "Create Server Backup" from the panel creates N backups (one per user), each with its own snapshot.

---

### Step 3: Admin UI — User-Filterable Backups Table (parallel with Step 4)

**Context**: The admin backups table shows all backups in one flat list. With per-account backups, admins need to filter by user.

**Files**:
- `app/Filament/Admin/Pages/Backups.php` — `backupsTable()` method

**Tasks**:
1. Add a `TextColumn::make('user.username')` column showing which user the backup belongs to
2. Add a `SelectFilter` for user filtering
3. Update "Create Server Backup" action to show progress (N users being backed up)
4. Group or sort by user by default
5. Download button already works — just verify it works with user-scoped snapshots
6. Remove the `snapshot_id` column (internal detail, not useful to admins)

**Verification**: Visit `/jabali-admin/backups`, verify user column and filter work

**Exit criteria**: Admin can see and filter backups by user, create server backup creates per-user entries.

---

### Step 4: Simplify Restore Wizard (parallel with Step 3)

**Context**: The restore wizard's Step 1 asks "Restore for user" with a dropdown. With per-account backups, the user is already known from the backup record. The wizard can skip this step.

**Files**:
- `app/Filament/Admin/Pages/RestoreBackup.php`

**Tasks**:
1. Auto-populate `selectedUser` from `$backup->user->username` in `mount()`
2. Remove the user selection dropdown from Step 1 (or make it read-only showing the backup's user)
3. Keep Step 1 for backup info display only
4. Verify the file browser and selective restore still work with user-scoped snapshots
5. Ensure `loadContents()` path matching works (paths are now relative to `/home/{user}` not containing all users)

**Verification**: Navigate to restore wizard from a per-user backup, verify it auto-selects the right user

**Exit criteria**: Restore wizard knows which user to restore without admin having to select.

---

### Step 5: User Panel Backups (serial, depends on Steps 1-2)

**Context**: The user panel backup page should show the user's own backups (both manual and from server schedules) and allow download/restore.

**Files**:
- `app/Filament/Jabali/Pages/Backups.php` (if exists, or check user panel backup pages)

**Tasks**:
1. Read the current user panel backup page
2. Ensure it queries `Backup::where('user_id', auth()->id())` to include server-schedule backups
3. Add download button (same streaming approach as admin)
4. Add restore action (simplified — no user selection needed, it's their own account)
5. Verify user can only see/download/restore their own backups (ownership check)

**Verification**: Log in as a non-admin user, verify backup list shows their backups, download works

**Exit criteria**: Users see all their backups (manual + server-scheduled) and can download/restore them.

---

### Step 6: Migration & Cleanup (serial, last)

**Context**: Existing server-wide backups need handling. Old bulk snapshots should still be browsable but marked as legacy.

**Files**:
- `database/migrations/` — new migration if needed
- `app/Services/Backup/BackupOrchestrator.php`

**Tasks**:
1. Add migration to mark existing null-user_id backups as `type = 'legacy_server'` or similar
2. Admin UI should still show legacy backups but with a "Legacy" badge
3. Remove or deprecate `backupCreateServer()` agent function (keep as alias that calls per-user)
4. Update backup schedule creation UI — "Server Backup" now means "all users individually"
5. Clean up any dead code paths for bulk snapshots

**Verification**: `php artisan test --compact`, verify legacy backups still display

**Exit criteria**: Clean transition from old to new, no data loss, legacy backups accessible.

---

## Dependency Graph

```
Step 1 (Agent)
  │
  v
Step 2 (Orchestrator) ──┬──> Step 3 (Admin UI)
                         │
                         └──> Step 4 (Restore Wizard)
                                │
                                v
                         Step 5 (User Panel)
                                │
                                v
                         Step 6 (Migration & Cleanup)
```

## Invariants (verify after every step)

- `php -l bin/jabali-agent` passes
- `php artisan test --compact` passes
- Existing backups remain visible in admin panel
- No hardcoded paths — all paths derived from username

## Rollback

Each step is independently revertable via git. The old `backupCreateServer()` is kept until Step 6, so falling back to bulk backups is always possible.
