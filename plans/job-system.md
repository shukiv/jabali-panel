# Blueprint: Job System for jabali-backup

## Objective

Replace ad-hoc background processes with a proper job system that uses the existing Jabali panel `backups` and `backup_restores` tables. All long-running operations (backup, restore, download) become trackable jobs with status, progress, and logs visible in the panel.

## Current State

| Operation | How it runs | Status tracking | Problem |
|-----------|-------------|-----------------|---------|
| Backup | `jbExecBackground` → nohup | None | No visibility, no error tracking |
| Restore | `jbExec` blocking (300s) | None | Timeout on large accounts, blocks UI |
| Download | `jbExecBackground` + token polling | `/tmp/*.json` files | Fragile, no cleanup, complex flow |

## Target State

| Operation | How it runs | Status tracking |
|-----------|-------------|-----------------|
| Backup | Agent writes `backups` row → runs CLI → updates row | DB: pending→running→completed/failed |
| Restore | Agent writes `backup_restores` row → runs CLI → updates row | DB: pending→running→completed/failed |
| Download | Agent writes `backups` row (type=download) → runs CLI → updates row | DB: pending→running→completed/failed |

Panel polls the DB row instead of agent calls. No more blocking, no more tokens.

## Existing DB Schema (already in Jabali)

### `backups` table
- `status`: pending / running / uploading / completed / failed
- `started_at`, `completed_at`, `error_message`
- `user_id`, `destination_id`, `schedule_id`
- `snapshot_id`, `size_bytes`, `file_count`
- `local_path` (for download archives)
- `type`: full / partial / server / account

### `backup_restores` table
- `status`: pending / downloading / running / completed / failed
- `progress` (0-100)
- `log` (text)
- `started_at`, `completed_at`, `error_message`
- `backup_id`, `snapshot_id`, `user_id`, `performed_by`
- Component flags: `restore_files`, `restore_databases`, etc.

## Architecture

```
Panel UI ──→ AgentClient.send('jb.backup_start') ──→ Agent handler
                                                        │
                                                        ├─ INSERT INTO backups (status='pending')
                                                        ├─ nohup jabali-backup run ... &
                                                        └─ return {job_id: 123}

Panel UI polls: SELECT status, progress FROM backups WHERE id = 123
                (via Livewire polling or manual refresh)

CLI on completion: UPDATE backups SET status='completed', size_bytes=..., snapshot_id=...
CLI on failure:    UPDATE backups SET status='failed', error_message=...
```

## Steps

### Step 1: Agent job handlers (create/update DB rows)

**Files:** `panel/agent/jabali-backup.php`

New agent RPC methods:
- `jb.job_backup_start` → INSERT into `backups`, run CLI in background, return job_id
- `jb.job_restore_start` → INSERT into `backup_restores`, run CLI in background, return job_id
- `jb.job_download_start` → INSERT into `backups` (type='partial'), run CLI in background, return job_id
- `jb.job_status` → SELECT from `backups` or `backup_restores` by id, return status/progress/log

The background process wrapper:
```bash
nohup bash -c '{CLI_COMMAND}; STATUS=$?; mysql jabali -e "UPDATE {table} SET status=IF($STATUS=0,\"completed\",\"failed\"), completed_at=NOW(), error_message=IF($STATUS!=0,\"exit $STATUS\",NULL) WHERE id={job_id}"' &
```

**Exit criteria:** Agent can create jobs, CLI updates them on completion.

### Step 2: Panel UI for job tracking

**Files:** `panel/filament/pages/Backups.php`, `panel/views/backups.blade.php`

Changes:
- Backup action → calls `jb.job_backup_start`, gets `job_id`, shows "Backup started" notification
- Restore action → calls `jb.job_restore_start`, gets `job_id`, shows "Restore started" notification  
- Download action → calls `jb.job_download_start`, gets `job_id`, shows "Download started" notification
- Add "Jobs" section (or inline status) showing recent jobs with status badges
- Download: when job status = completed and `local_path` exists, show download link to `backup-download.php`

**Exit criteria:** All operations create DB jobs, status visible in panel.

### Step 3: CLI job status updates

**Files:** `bin/jabali-backup`

Add `--job-id=N --job-table=TABLE` flags to `cmd_run`, `cmd_restore`, `cmd_download`:
- On start: UPDATE status='running', started_at=NOW()
- On progress (optional): UPDATE progress=N
- On success: UPDATE status='completed', completed_at=NOW(), snapshot_id=..., size_bytes=...
- On failure: UPDATE status='failed', error_message=...

**Exit criteria:** CLI writes status updates to DB during execution.

### Step 4: Download via job system

**Files:** `panel/public/backup-download.php`, `panel/agent/jabali-backup.php`

- Download job creates archive at `/tmp/jabali-download-{job_id}.tar.gz`
- CLI sets `local_path` in `backups` row on completion
- `backup-download.php` accepts `?job_id=N` instead of token, validates status=completed and local_path exists
- Panel shows download link when job is complete

**Exit criteria:** Downloads work through job system, no more token polling.

## Dependency Order

```
Step 1 (agent handlers) → Step 3 (CLI updates) → Step 2 (panel UI) → Step 4 (download)
         │                        │
         └── can test via CLI ────┘
```

Steps 1+3 can be developed together. Step 2 depends on both. Step 4 depends on Step 2.

## What NOT to change

- The CLI backup/restore/download logic itself — just add job-id status updates around it
- The restic integration — unchanged
- The collector/restorer scripts — unchanged
- The Jabali panel codebase — we only INSERT/UPDATE rows it already defines
