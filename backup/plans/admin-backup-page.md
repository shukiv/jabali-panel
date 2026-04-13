# Blueprint: Admin Backups Page for Jabali Panel

Build the admin Backups page in the Jabali panel that wraps the existing
agent backup RPC routes (backup.create, backup.list, backup.restore, etc.)
into a Filament UI with snapshot browsing, granular restore, and scheduling.

## Current State

### Agent RPC routes already implemented (bin/jabali-agent):

| Route | Purpose | Line |
|-------|---------|------|
| backup.create | Create backup for a user | 16182 |
| backup.restore | Restore from snapshot (files, DBs, mail, per-domain) | 16708 |
| backup.list | List snapshots (optionally per user) | 16964 |
| backup.list_contents | List all files in a snapshot | 16991 |
| backup.list_domain_files | Browse files at a path in a snapshot | 17018 |
| backup.get_info | Get snapshot metadata | 17052 |
| backup.delete | Delete a snapshot | 17095 |
| backup.verify | Verify snapshot integrity | 17134 |
| backup.read_snapshot_file | Read a single file from snapshot | 17235 |
| backup.export_snapshot | Export snapshot as downloadable archive | 17273 |
| backup.test_destination | Test backup destination connectivity | 789 |
| backup.get_password | Get restic password | 791 |
| backup.set_password | Set restic password | 792 |

### Jabali panel conventions (from Domains.php pattern):

- Page class: extends Page, implements HasActions + HasForms + HasTable
- Traits: InteractsWithActions, InteractsWithForms, InteractsWithTable
- Agent calls: `app(AgentClient::class)->send('action', $params)`
- Labels: `__('...')` for i18n
- Icons: Heroicons `heroicon-o-*`
- Errors: `SafeError::message($e)`
- Notifications: `Notification::make()->title()->body()->success()->send()`
- Views: `resources/views/filament/admin/pages/{name}.blade.php`
- Navigation: `$navigationSort`, `$navigationIcon`, `getNavigationLabel()`

### Files that DON'T exist yet:

- `app/Filament/Admin/Pages/Backups.php` (needs creation)
- `resources/views/filament/admin/pages/backups.blade.php` (needs creation)

### Models that already exist (app/Models/):

- BackupDestination.php
- BackupSchedule.php
- Backup.php
- BackupRestore.php
- UserRemoteBackup.php

## Architecture

The page uses **tabs** to organize backup features:

```
Backups Page (admin)
  Tab: Snapshots       Table of restic snapshots with actions
  Tab: Restore         Granular restore with file browser
  Tab: Schedules       Manage automated backup schedules
  Tab: Settings        Destinations, passwords, retention
```

All operations go through `AgentClient->send()` to the existing agent
backup RPC routes. No direct CLI calls from the panel.

## Steps

### Step 0: Page Skeleton + Snapshots Tab

Depends on: nothing

Context: Create the admin Backups page with the Snapshots tab. This is the
main view showing all backup snapshots across all users in a table.

Files to create:
- app/Filament/Admin/Pages/Backups.php
- resources/views/filament/admin/pages/backups.blade.php

Tasks:

1. Create `app/Filament/Admin/Pages/Backups.php`:
   - Class: `Backups extends Page implements HasActions, HasForms, HasTable`
   - Traits: InteractsWithActions, InteractsWithForms, InteractsWithTable
   - Navigation: icon `heroicon-o-cloud-arrow-up`, sort 11, label `__('Backups')`
   - View: `filament.admin.pages.backups`
   - Properties: `public string $activeTab = 'snapshots'`
   - Method `mount()`: load snapshots via agent
   - Method `table()`: show snapshots from agent's `backup.list` response
     - Column: ID (short_id)
     - Column: Account (parsed from tags `account:xxx`)
     - Column: Date (formatted from `time` field)
     - Column: Size (from snapshot data if available)
     - Column: Tags (badge list)
     - Filter: User (select from User model)
     - Filter: Date range
     - Row actions: Browse, Restore, Delete, Verify

2. Create Blade view with tabs:
   ```blade
   <x-filament-panels::page>
       <x-filament::tabs>
           <x-filament::tabs.item :active="$activeTab === 'snapshots'"
               wire:click="$set('activeTab', 'snapshots')">
               {{ __('Snapshots') }}
           </x-filament::tabs.item>
           <!-- more tabs added in later steps -->
       </x-filament::tabs>

       @if($activeTab === 'snapshots')
           {{ $this->table }}
       @endif
   </x-filament-panels::page>
   ```

3. Header actions:
   - "Create Backup" button with modal form:
     - Select: account (from User model, searchable)
     - Toggles: include_files, include_databases, include_email, include_ssl, include_dns
     - Calls `backup.create` via agent in background
   - "Refresh" button to reload snapshot list

4. The snapshot table needs a custom query since data comes from the agent,
   not Eloquent. Two approaches:
   a. Use a Livewire property `$snapshots` and render manually in Blade
   b. Sync snapshots into a `backup_snapshots` cache table for native Filament table

   Approach (a) is simpler for v1. The Blade view renders the table manually
   from `$this->snapshots`, with Livewire actions for row buttons.

Verification:
- Navigate to /jabali-admin/backups
- See list of snapshots with user, date, size
- "Create Backup" opens modal, submits, shows notification
- "Refresh" reloads list

Exit criteria: Admin can view snapshots and trigger backups from the UI.

### Step 1: Browse Snapshot (File Explorer)

Depends on: Step 0

Context: When admin clicks "Browse" on a snapshot, show a file browser
that lets them navigate directories and see file details. This uses the
existing `backup.list_domain_files` agent route.

Tasks:

1. Add method `browseSnapshot(string $snapshotId, string $username)`:
   - Sets `$this->activeTab = 'browser'`
   - Sets `$this->browseSnapshotId`, `$this->browseUsername`, `$this->browsePath = ''`
   - Calls agent `backup.list_domain_files` with path=''
   - Stores result in `$this->browseItems`

2. Add method `navigateTo(string $path)`:
   - Updates `$this->browsePath`
   - Calls agent `backup.list_domain_files` with new path
   - Refreshes `$this->browseItems`

3. Add method `navigateUp()`:
   - Goes to parent directory

4. Add Blade section for browser tab:
   - Breadcrumb bar: home > domains > example.com > public_html
   - Table of items: icon (folder/file), name (clickable if dir), size, modified, checkbox
   - Selected items bar at bottom: "3 files selected" + "Restore Selected" button

5. Agent data format (from backupListDomainFiles):
   ```json
   {
     "items": [
       {"name": "public_html", "path": "home/user/domains/example.com/public_html",
        "is_dir": true, "size": 0, "permissions": "drwxr-xr-x", "modified": 1712345678}
     ]
   }
   ```

Verification:
- Click "Browse" on a snapshot row
- See file listing with folders and files
- Click folder to navigate deeper
- Breadcrumb shows current path
- Can select files for restore

Exit criteria: Admin can browse any snapshot's file tree.

### Step 2: Granular Restore Modal

Depends on: Step 0, Step 1

Context: The restore flow. Two entry points:
1. "Restore" action on a snapshot row (full restore)
2. "Restore Selected" from the file browser (file-level restore)

Tasks:

1. Add "Restore" row action on snapshot table:
   - Modal form with:
     - Info card: snapshot ID, date, account
     - Scope section:
       - Toggle: restore_files (default: on)
       - Toggle: restore_databases (default: on)
       - Toggle: restore_mailboxes (default: on)
       - Toggle: restore_ssl (default: on)
       - Toggle: restore_dns (default: on)
     - Granularity section (optional, collapsed by default):
       - Multi-select: selected_domains (populated from snapshot contents)
       - Multi-select: selected_databases
     - Toggle: force (overwrite existing, default: off)
     - Warning text when force is on
   - Calls agent `backup.restore` with selected options
   - Shows progress notification

2. Add "Restore Selected Files" action from browser:
   - Collects selected paths from `$this->selectedBrowseItems`
   - Modal confirmation: "Restore N files to /home/{user}/?"
   - Toggle: force (overwrite)
   - Calls agent with `restore_files=true`, `selected_domains=[paths]`

3. Add restore log viewer:
   - After restore starts, show log output in a textarea or scrollable div
   - Poll for completion status

Verification:
- Click "Restore" on a snapshot, see modal with component toggles
- Toggle off databases, click Restore, see notification
- From browser, select 2 files, click "Restore Selected", confirm, see result

Exit criteria: Admin can do full restore, selective component restore, and file-level restore.

### Step 3: Delete, Verify, Export Actions

Depends on: Step 0

Context: Additional snapshot management actions.

Tasks:

1. Add "Delete" row action:
   - Confirmation modal: "Delete snapshot {id}? This cannot be undone."
   - Calls agent `backup.delete` with snapshot_id
   - Refreshes list on success

2. Add "Verify" row action:
   - Calls agent `backup.verify` with snapshot_id
   - Shows result notification (integrity OK / errors found)

3. Add "Export" row action:
   - Calls agent `backup.export_snapshot` with snapshot_id + username
   - Returns download URL or streams the file
   - Opens download in browser

4. Add bulk actions:
   - Bulk delete selected snapshots
   - Bulk verify

Verification:
- Delete a snapshot, confirm it's gone
- Verify a snapshot, see "Integrity OK"
- Export a snapshot, download starts

Exit criteria: All CRUD operations work on snapshots.

### Step 4: Schedules Tab

Depends on: Step 0

Context: Manage automated backup schedules using the BackupSchedule model
that already exists in the Jabali database.

Tasks:

1. Add Schedules tab to Blade view
2. Add `scheduleTable()` method (or use a second table with `$activeTab` switching)
3. Schedule list table:
   - Columns: name, scope (all/selected users), frequency, time, destination, enabled toggle
   - Actions: edit, run now, delete
4. "Create Schedule" header action:
   - Form sections matching PANEL-INTEGRATION.md design:
     - General: name, enabled
     - Scope: all / selected users (multi-select)
     - Schedule: frequency (daily/weekly/monthly), time, day_of_week, day_of_month
     - Components: checkboxes for files, databases, email, ssl, dns, nginx, php, cron
     - Retention: keep_last, keep_daily, keep_weekly, keep_monthly
5. "Run Now" action:
   - Dispatches backup job with schedule config
   - Shows "Backup started" notification

Since BackupSchedule is an Eloquent model, this tab CAN use native Filament
table with `->query(BackupSchedule::query())`.

Verification:
- Create a schedule, see it in the list
- Toggle enabled on/off
- Click "Run Now", see backup start
- Edit schedule, change time, save

Exit criteria: Admin can CRUD backup schedules.

### Step 5: Settings Tab (Destinations + Config)

Depends on: Step 0

Context: Manage backup destinations (BackupDestination model) and
restic configuration (password, retention defaults).

Tasks:

1. Add Settings tab to Blade view
2. Destinations section:
   - Table from BackupDestination model
   - Columns: name, type, path, status (reachable/failed)
   - "Add Destination" action with form:
     - name, type (select: local/sftp/s3/wasabi/b2/gcs/rclone)
     - Conditional fields based on type (host, port, username, key, path, bucket, etc.)
   - "Test Connection" button: calls agent `backup.test_destination`
   - Edit, delete actions

3. Config section:
   - Restic password management (set/change via `backup.set_password`)
   - Default retention policy (keep_last, keep_daily, keep_weekly, keep_monthly)
   - These settings stored in the Jabali `settings` table or `.env`

Verification:
- Add an SFTP destination, test it, see "Connection OK"
- Change retention defaults, see them saved
- Delete a destination

Exit criteria: Admin can manage backup destinations and configuration.

### Step 6: User Backups Page (Jabali panel)

Depends on: Steps 0-3

Context: Create the user-facing Backups page. Similar to admin but scoped
to the logged-in user. Users can view their snapshots, browse files, and
restore their own data.

Files to create:
- app/Filament/Jabali/Pages/Backups.php
- resources/views/filament/jabali/pages/backups.blade.php

Tasks:

1. Create user Backups page:
   - Same structure as admin but:
     - Always filter by `auth()->user()->username`
     - No Schedules or Settings tabs (admin only)
     - No "Create Backup" for other users
     - "Request Backup" button (triggers backup for own account)
   - Tabs: Snapshots, Browse, Restore

2. Security:
   - ALWAYS use `auth()->user()->username` for agent calls
   - NEVER accept username from user input
   - Guard all agent calls with user scope

Verification:
- Login as regular user
- See only own snapshots
- Browse own backup files
- Restore own files

Exit criteria: Users can view and restore their own backups.

## Dependency Graph

```
Step 0 (skeleton + snapshots)
  Step 1 (file browser)
  Step 3 (delete, verify, export)   Parallel
  Step 4 (schedules tab)
  Step 5 (settings/destinations)
       |
  Step 2 (granular restore, needs Step 0 + Step 1)
       |
  Step 6 (user page, reuses Steps 0-3 patterns)
```

Maximum parallelism: Steps 1, 3, 4, 5 are independent after Step 0.

## Key Implementation Notes

### Snapshot data is NOT in Eloquent

The agent returns snapshots from restic as JSON arrays, not from the database.
For the Filament table, either:

a) **Manual Blade table** (simpler, recommended for v1):
   Store snapshots in a Livewire property, render with Blade loop.
   Use `wire:click` for row actions instead of Filament `recordActions()`.

b) **Sync to cache table** (better UX, needed for sorting/filtering):
   Create a `backup_snapshots` table, sync periodically via artisan command.
   Then use native `->query(BackupSnapshot::query())` for the table.

### Long operations must be async

`backup.create` and `backup.restore` can take minutes. The agent runs them
in background and returns immediately. The UI should:
1. Show "Started..." notification
2. Poll for status or show a log viewer
3. Refresh table when complete

### Blade view template

The backups page needs more custom Blade than most Filament pages because
of the file browser. Use Alpine.js for interactive bits (tree navigation,
file selection, breadcrumbs).

### All labels wrapped in __()

The panel supports 8 languages. Every user-facing string must use `__()`.

## Invariants

1. User panel ALWAYS scopes to auth()->user()->username
2. All agent calls validate input (username, snapshot_id format)
3. Delete/restore actions require confirmation modal
4. Long operations run async with progress feedback
5. Errors sanitized with SafeError::message() before display
6. No hardcoded credentials in source
