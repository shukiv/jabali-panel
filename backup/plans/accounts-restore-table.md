# Blueprint: Accounts-Based Restore / Download Table

**Objective:** Replace the snapshots-based Restore/Download tab with an accounts table. Each row is an account with its latest snapshot info. Single-account selection opens granular restore (component checkboxes, file/database picker). Multi-account selection offers full restore.

**Current state:** Snapshots table with per-row Restore/Download wizard + Browse button. Rows are individual restic snapshots (ID, account, date).

**Target state:** Accounts table where each row shows the account + latest backup date + snapshot count. Single-account row actions give granular control down to individual files/databases. Multi-select bulk action offers full restore with component checkboxes.

---

## Step 1: Restructure snapshotsTable → accountsTable

### Context Brief
The Restore/Download tab currently calls `snapshotsTable()` which lists raw restic snapshots. Replace it with an `accountsTable()` that groups by account. The data source changes from `$this->snapshots` (flat list of snapshots) to `$this->backupAccounts` (one entry per account with latest snapshot info).

### Files
- `panel/filament/pages/Backups.php`
- `panel/agent/jabali-backup.php`

### Tasks

1. **Add `jb.backup_accounts` agent route** in `panel/agent/jabali-backup.php`:
   - Calls `jabali-backup list snapshots` via `jbExec()`
   - Groups snapshots by `account:` tag
   - Returns one entry per account: `{username, snapshot_count, latest_snapshot_id, latest_date}`
   - Reuses existing `jbListSnapshots()` output, just aggregates it

2. **Add `loadBackupAccounts()` method** in `Backups.php`:
   - Calls `jb.backup_accounts` agent route
   - Stores in `public array $backupAccounts = []`
   - Calls `$this->resetTable()`

3. **Replace `snapshotsTable()` with `accountsTable()`** in `Backups.php`:
   - Columns:
     - Account (username, badge)
     - Latest Snapshot (date)
     - Snapshots (count badge)
   - Per-row actions (single account):
     - **Restore** — wizard: snapshot selector → component checkboxes → force toggle → execute
     - **Download** — wizard: snapshot selector → component checkboxes → execute download
     - **Browse** — links to SnapshotBrowser page
   - Bulk actions (multi-account):
     - **Restore All** — component checkboxes, uses "latest" snapshot for each, force toggle
     - **Download All** — component checkboxes, archives all selected accounts
   - Toolbar actions:
     - Refresh

4. **Update `table()` dispatch**: Change `restore_download` branch from `snapshotsTable($table)` to `accountsTable($table)`

5. **Update `updatedActiveTab()`**: Change `restore_download` case from `loadSnapshots()` to `loadBackupAccounts()`

6. **Single-account Restore wizard** (per-row action):
   ```
   Step 1: Select Snapshot (dropdown of all snapshots for this account, default: latest)
   Step 2: What to Restore (CheckboxList — matching reference image)
           - Panel Config (metadata)
           - Home Dir Files (files)  
           - Databases (mysql)
           - Email Accounts (email)
           - Domains (dns)
           - Cron Jobs (cron)
           - SSL Certificates (ssl)
           - Nginx Config (nginx)
           - PHP Config (php)
           columns(3), all checked by default
   Step 3: Options
           - Overwrite existing data (Toggle, default: off)
   → Execute: calls jb.restore with selected components
   ```

7. **Single-account Download wizard** (per-row action):
   ```
   Step 1: Select Snapshot
   Step 2: What to Download (same CheckboxList)
   → Execute: calls jb.download, shows progress, returns download link
   ```

8. **Multi-account Restore bulk action**:
   ```
   Modal: Component checkboxes (same list) + Force toggle
   → Execute: loops selected accounts, restores each with "latest" snapshot
   ```

9. **Multi-account Download bulk action**:
   ```
   Modal: Component checkboxes
   → Execute: calls jb.download with all selected usernames
   ```

### Verification
- Open Restore/Download tab → see accounts table (not snapshots)
- Single account → Restore wizard with snapshot picker + component checkboxes
- Select 2+ accounts → Bulk Restore with component checkboxes
- Download works for single and multi-account

### Exit Criteria
- Table rows are accounts, not snapshots
- Single-account restore has granular component selection
- Multi-account offers full restore with components
- Download flow works for both modes
- No custom CSS — only Filament components

---

## Step 2: Granular File/Database Restore for Single Account

### Context Brief
When a single account is selected, the user should be able to drill down into individual files or databases to restore. This extends the single-account restore with a "Browse & Restore" option that uses the existing SnapshotBrowser, and a database picker that lists available databases.

### Files
- `panel/filament/pages/Backups.php`
- `panel/agent/jabali-backup.php`

### Tasks

1. **Add "Browse & Restore Files" row action** on the accounts table:
   - Links to SnapshotBrowser (`/jabali-admin/backups-browse?snapshot=LATEST_ID&user=USERNAME`)
   - SnapshotBrowser already has "Restore Selected" bulk action from earlier work

2. **Add `jb.list_databases` agent route**:
   - Calls `jabali-backup` to list databases for a user from the snapshot
   - Returns array of database names found in the backup

3. **Add "Restore Databases" row action** (optional granular restore):
   - Opens modal with CheckboxList of databases from `jb.list_databases`
   - Snapshot selector dropdown
   - Calls `jb.restore` with `--only=mysql` and selected databases
   - Or uses `jb.restore_files` targeting specific database dump files

4. **Ensure Browse action passes latest snapshot for the account**:
   - The row record has `latest_snapshot_id` — use it in the Browse URL

### Verification
- Click Browse on account row → opens SnapshotBrowser for latest snapshot
- Select files in browser → Restore Selected works
- Database restore modal shows databases from backup

### Exit Criteria
- Single-account granular file restore via SnapshotBrowser
- Browse button on each account row
- Files and databases can be individually selected for restore

---

## Dependency Graph

```
Step 1 (Accounts table + wizards) ──> Step 2 (Granular file/db restore)
```

Step 1 is the core restructure. Step 2 adds drill-down granularity.

## Summary

| Step | Description | Files |
|------|------------|-------|
| 1 | Accounts table replacing snapshots table, single/multi wizards | `Backups.php`, `jabali-backup.php` |
| 2 | Granular file/database restore per account | `Backups.php`, `jabali-backup.php` |

**Total steps:** 2 (sequential)
