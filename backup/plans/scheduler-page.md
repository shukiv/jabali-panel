# Blueprint: Scheduler Page — Multi-Job Backup Scheduling

**Objective:** Replace the current single-cron schedule tab with a multi-job scheduler. Each job links to a destination, selects accounts (all or specific), has an exclude list, and runs on a cron schedule. Jobs are stored in `/etc/jabali-backup/schedules.json` and managed by CLI subcommands. The panel wraps the CLI.

**Current state:** Single cron entry via `cmd_schedule` that runs `jabali-backup run` for all accounts. The Schedule tab has Install/Remove actions with a frequency preset modal.

**Target state:** Multiple named schedule jobs stored in `schedules.json`. Each job specifies: destination, accounts (all or list), excludes, cron expression, enabled/disabled. The Schedule tab shows a Filament table of jobs with Add/Edit/Delete/Enable/Disable actions. Crontab has one entry per enabled job.

---

## Step 1: CLI `schedule` Subcommand Rewrite + JSON Storage

### Context Brief

The CLI currently has a basic `cmd_schedule` that manages a single crontab entry. Rewrite it to support multiple named schedule jobs stored in `/etc/jabali-backup/schedules.json`. Each job is a cron entry that calls `jabali-backup run` with the right `--destination` and account flags.

**Key rule:** Panel wraps CLI — all logic lives here first.

### Schema: `/etc/jabali-backup/schedules.json`

```json
[
  {
    "id": "daily-all-rasp",
    "name": "Daily to Rasp",
    "destination": "rasp",
    "cron": "0 2 * * *",
    "accounts": "all",
    "exclude": "testuser,demouser",
    "enabled": true,
    "created_at": "2026-04-05T20:00:00Z"
  },
  {
    "id": "weekly-alice-local",
    "name": "Weekly Alice to Local",
    "destination": "local-backup",
    "cron": "0 3 * * 0",
    "accounts": "alice,bob",
    "exclude": "",
    "enabled": false,
    "created_at": "2026-04-05T20:00:00Z"
  }
]
```

### Tasks

1. **Rewrite `cmd_schedule` with subcommands:**
   - `schedule list [--json]` — List all schedule jobs (table or JSON)
   - `schedule add --name=NAME --destination=ID --cron=EXPR [--accounts=all|user1,user2] [--exclude=user3,user4]` — Add a new job
   - `schedule update --id=ID [--name=...] [--cron=...] [--accounts=...] [--exclude=...] [--destination=...]` — Update a job
   - `schedule remove --id=ID` — Remove a job
   - `schedule enable --id=ID` — Enable a job (add to crontab)
   - `schedule disable --id=ID` — Disable a job (remove from crontab)
   - `schedule sync` — Rebuild crontab from all enabled jobs
   - `schedule show --id=ID` — Show job details (JSON)

2. **Crontab integration:**
   - Each enabled job gets its own crontab line with a unique marker: `# jabali-backup:job-id`
   - The cron command is: `jabali-backup run --destination=<dest> [username1 username2 | --exclude=user3,user4] >> /var/log/jabali-backup.log 2>&1`
   - `schedule sync` rebuilds all crontab entries from enabled jobs
   - `schedule enable/disable` calls `sync` after toggling

3. **Update `cmd_run` to support multiple usernames and `--exclude`:**
   - `jabali-backup run alice bob charlie` — back up specific accounts
   - `jabali-backup run --exclude=testuser,demo` — back up all except listed
   - This is needed so cron lines can target specific accounts

4. **JSON storage helpers:**
   - Use same atomic write pattern as destinations: write to `.tmp`, rename
   - Same `_gen_id()` slug generation from name

5. **Backward compat:**
   - If `schedules.json` doesn't exist, `schedule list` shows the legacy single cron entry (if any)
   - `schedule install` / `schedule remove` still work as aliases for quick single-job setup

### Verification
```bash
jabali-backup schedule add --name="Daily All" --destination=rasp --cron="0 2 * * *"
jabali-backup schedule add --name="Weekly Alice" --destination=rasp --cron="0 3 * * 0" --accounts=alice
jabali-backup schedule list
jabali-backup schedule enable --id=daily-all
crontab -l | grep jabali-backup
jabali-backup schedule disable --id=daily-all
```

### Exit Criteria
- Multiple schedule jobs stored in schedules.json
- Crontab entries created/removed per job
- `cmd_run` supports multiple usernames and `--exclude`
- Backward compat with old single-cron usage

---

## Step 2: Agent Routes Wrapping CLI

### Context Brief

Add agent routes that wrap the new `schedule` CLI subcommands. These are thin wrappers calling `jbExec(['schedule', ...])`.

### Tasks

1. **Replace existing schedule routes with new ones:**
   - `jb.schedules_list` → `schedule list --json`
   - `jb.schedules_add` → `schedule add --name=... --destination=... --cron=... [--accounts=...] [--exclude=...]`
   - `jb.schedules_update` → `schedule update --id=... [fields]`
   - `jb.schedules_remove` → `schedule remove --id=...`
   - `jb.schedules_enable` → `schedule enable --id=...`
   - `jb.schedules_disable` → `schedule disable --id=...`

2. **Keep backward compat:**
   - `jb.schedule` (old route) can map to `jb.schedules_list` for read, or keep for legacy callers

### Exit Criteria
- All agent routes work via socket
- Each route is a thin CLI wrapper via `jbExec()`

---

## Step 3: Filament Schedule Tab — Table + Add Job Modal

### Context Brief

Replace the current Schedule tab (single Install/Remove actions) with a Filament table of schedule jobs. The table switches via `$this->activeTab === 'schedule'` in the `table()` method, same pattern as destinations/snapshots.

### Tasks

1. **Add `schedulesTable()` method to Backups.php:**
   - Columns: Name, Destination (badge), Accounts, Cron, Status (Enabled/Disabled badge)
   - Record actions: Edit, Enable/Disable toggle, Remove
   - Toolbar actions: Add Job, Refresh

2. **Add Job modal form:**
   - `name` — TextInput (required)
   - `destination` — Select populated from `$this->destinations` (required)
   - `cron` — Select with presets (Daily 2AM, 3AM, 4AM, Every 6h, Every 12h, Weekly Sun, Weekly Mon, Monthly, Custom) + TextInput for custom
   - `accounts` — Select: "All accounts" or "Specific accounts"
   - `specific_accounts` — Select multiple, populated from users (visible when "Specific accounts" selected)
   - `exclude` — TextInput (comma-separated usernames to exclude)

3. **Edit Job modal:** Same form, pre-filled from record data

4. **Enable/Disable toggle:** Calls agent route, refreshes table

5. **Update `table()` method:**
   ```php
   if ($this->activeTab === 'destination') return $this->destinationsTable($table);
   if ($this->activeTab === 'schedule') return $this->schedulesTable($table);
   return $this->snapshotsTable($table);
   ```

6. **Update Blade view:** Replace the old schedule section with `{{ $this->table }}`

7. **Remove old schedule methods:** `installScheduleAction`, `removeScheduleAction`, `loadSchedule`, `scheduleStatus`, `scheduleCron`

### Verification
- Open Schedule tab → see table with Add Job button
- Add a job → appears in table
- Enable → crontab entry created
- Disable → crontab entry removed
- Edit → modal pre-filled, save updates
- Delete → removed from table and crontab

### Exit Criteria
- Schedule tab shows Filament table of jobs
- Add/Edit/Delete/Enable/Disable all work
- No custom CSS — all Filament components
- Table refreshes after mutations

---

## Dependency Graph

```
Step 1 (CLI) ──> Step 2 (Agent) ──> Step 3 (Filament UI)
```

All sequential — each step depends on the previous.

## Summary

| Step | Description | Files |
|------|------------|-------|
| 1 | CLI schedule rewrite + schedules.json + cmd_run --exclude | `bin/jabali-backup` |
| 2 | Agent route wrappers | `panel/agent/jabali-backup.php` |
| 3 | Filament table + Add/Edit/Enable/Disable modals | `Backups.php`, `backups.blade.php` |

**Total steps:** 3 (sequential)
