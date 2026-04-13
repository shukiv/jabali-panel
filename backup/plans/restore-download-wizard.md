# Blueprint: Restore / Download Wizard

**Objective:** Replace the current Restore/Download tab (snapshots table + inline restore sections) with a wizard flow. The wizard guides the admin through: select accounts → choose Restore or Download → configure what to restore/download → execute.

**Current state:** Snapshots table with per-row Browse/Restore actions. Restore opens inline sections with checkboxes for Files/Databases/Email + force toggle. No download capability.

**Target state:** A multi-step wizard using Filament Actions wizard steps. No separate snapshots table — the wizard IS the tab content.

---

## Wizard Flow

### Step 1: Select Accounts
- Multi-select of accounts (from User model)
- Each shows latest snapshot date if available
- "Select All" option

### Step 2: Choose Action
- **Restore** — Restore backup data to live system
- **Download** — Package backup data as zip for download

### Step 3a: Restore (single account)
**"What to Restore"** — CheckboxList (matching the reference image):
- Panel Config (metadata)
- Home Dir Files (files)
- Databases (mysql)
- Database Users (mysql grants)
- Email Accounts (email)
- Domains (dns)
- Cron Jobs (cron)
- SSL Certificates (ssl)
- Nginx Config (nginx)
- PHP Config (php)

**"Restore Options":**
- Overwrite existing data (--force)

**Snapshot selector** — dropdown of available snapshots for this account (default: latest)

### Step 3a: Restore (multiple accounts)
- Same component checkboxes as single account
- Restore Options: Overwrite existing data
- Snapshot: always uses "latest" for each account
- Executes restore for each selected account sequentially

### Step 3b: Download
- For single or multiple accounts
- CLI creates a tar/zip of the restic-extracted data
- Agent returns a secured temporary download link
- UI shows progress → download link when ready

---

## Step 1: CLI `download` Subcommand

### Context Brief
The CLI needs a `download` command that extracts backup data to a temp dir and creates a downloadable archive. The panel wraps this.

### Tasks

1. **Add `cmd_download` to `bin/jabali-backup`:**
   ```
   jabali-backup download <username> [--snapshot=latest] [--only=...] [--output=/tmp/backup-username.tar.gz]
   ```
   - Extracts snapshot to temp dir (same as restore first phase)
   - Creates tar.gz of the extracted data
   - Outputs the archive path
   - For multiple users: `jabali-backup download alice bob --output=/tmp/backup-multi.tar.gz`

2. **Add `jb.download` agent route:**
   - Calls `jabali-backup download` via `jbExecBackground()`
   - Returns `{success: true, archive: "/tmp/...", log: "/tmp/..."}`
   - A separate `jb.download_status` route checks if the archive is ready
   - A `jb.download_link` route generates a time-limited download URL

3. **Secure download serving:**
   - Archive is written to a temp dir with random name
   - Agent creates a signed token (HMAC of path + timestamp)
   - Download served via agent route that validates token + expiry
   - Auto-cleanup after download or 1-hour expiry

### Verification
```bash
jabali-backup download shuki --output=/tmp/test-download.tar.gz
ls -la /tmp/test-download.tar.gz
```

### Exit Criteria
- `jabali-backup download` creates tar.gz archive
- Agent routes for background download + status + link
- Secure token-based download serving

---

## Step 2: Filament Wizard — Restore & Download

### Context Brief
Replace the Restore/Download tab content with a Filament wizard. The wizard uses Filament's `Action::make()->steps([...])` or a custom Livewire wizard flow.

**Key rule:** Only Filament components, no custom CSS.

### Tasks

1. **Replace tab content with wizard action:**
   - Remove snapshots table from restore_download tab
   - Add a "Start Restore / Download" toolbar action or auto-show wizard
   - The wizard has 3 steps

2. **Step 1 — Select Accounts:**
   ```php
   Select::make('accounts')
       ->label('Select Accounts')
       ->multiple()
       ->options(fn () => User::where('is_active', true)->pluck('username', 'username'))
       ->required()
   ```

3. **Step 2 — Choose Action:**
   ```php
   Select::make('action')
       ->label('What do you want to do?')
       ->options([
           'restore' => 'Restore to server',
           'download' => 'Download backup archive',
       ])
       ->required()
   ```

4. **Step 3 — Configure:**
   **For Restore:**
   ```php
   CheckboxList::make('components')
       ->label('What to Restore')
       ->options([
           'metadata' => 'Panel Config',
           'files' => 'Home Dir Files',
           'mysql' => 'Databases',
           'email' => 'Email Accounts',
           'dns' => 'Domains',
           'cron' => 'Cron Jobs',
           'ssl' => 'SSL Certificates',
           'nginx' => 'Nginx Config',
           'php' => 'PHP Config',
       ])
       ->columns(3)
       ->default(['metadata', 'files', 'mysql', 'email', 'dns', 'cron', 'ssl', 'nginx', 'php'])

   Select::make('snapshot')
       ->label('Snapshot')
       ->options(fn ($get) => /* load snapshots for selected accounts */)
       ->default('latest')
       ->visible(fn ($get) => count($get('accounts') ?? []) === 1)

   Toggle::make('force')
       ->label('Overwrite existing data')
   ```

   **For Download:**
   - Same component checkboxes
   - Submit triggers background archive creation
   - Shows progress indicator
   - When ready, shows download link button

5. **Execute:**
   - Restore: calls `jb.restore` for each account with selected components
   - Download: calls `jb.download`, polls `jb.download_status`, shows link

6. **Remove old code:**
   - Remove `snapshotsTable()` from table switching
   - Remove inline restore/browse sections from Blade
   - Remove old restore state properties

### Verification
- Open Restore/Download tab → wizard starts
- Select accounts → choose Restore → select components → execute
- Select accounts → choose Download → wait → download link appears

### Exit Criteria
- Wizard-based restore/download flow
- Component checkboxes matching the reference image
- Download creates archive with secure link
- All Filament components, no custom CSS

---

## Dependency Graph

```
Step 1 (CLI download) ──> Step 2 (Filament wizard)
```

## Summary

| Step | Description | Files |
|------|------------|-------|
| 1 | CLI `download` command + agent routes | `bin/jabali-backup`, `panel/agent/jabali-backup.php` |
| 2 | Filament wizard replacing restore tab | `Backups.php`, `backups.blade.php` |

**Total steps:** 2 (sequential)
