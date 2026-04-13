# Blueprint: Multi-Destination Table for Backups Page

**Objective:** Replace the single-destination info card in the Destination tab with a table of configured backup destinations. Add an "Add Destination" button that lets the admin add destinations of various types (Local, SFTP, SSH, S3, Wasabi, B2, Google Drive, Rclone). Each destination is stored in a JSON config file and can be initialized, tested, edited, or removed.

**Current state:** Single `[repository]` section in `/etc/jabali-backup/config.conf` with one type+path. The agent route `jb.destination` reads this single config. The Blade view shows a static info card.

**Target state:** A destinations JSON file (`/etc/jabali-backup/destinations.json`) holds an array of named destinations. The Destination tab shows a Filament-style table with columns: Name, Type, Path/URL, Status, Size. Row actions: Test, Edit, Delete. Header action: "Add Destination" modal. The **primary** destination (first one, or marked default) is used by `jabali-backup run`. The CLI falls back to `config.conf [repository]` if `destinations.json` doesn't exist (backward compat).

---

## Step 1: Destination Storage — JSON Config File + Agent CRUD Routes

**Branch:** `feat/multi-destination-storage`

### Context Brief
jabali-backup currently stores one repository backend in `/etc/jabali-backup/config.conf` under `[repository]`. We need a multi-destination model stored in `/etc/jabali-backup/destinations.json`. The agent addon (`panel/agent/jabali-backup.php`) needs CRUD routes for destinations.

### Tasks

1. **Define the destinations.json schema:**
   ```json
   [
     {
       "id": "uuid-or-slug",
       "name": "My S3 Backup",
       "type": "s3",
       "path": "s3:s3.amazonaws.com/bucket-name",
       "is_default": true,
       "credentials": {
         "access_key_id_file": "/etc/jabali-backup/s3-key",
         "secret_access_key_file": "/etc/jabali-backup/s3-secret"
       },
       "retention": {
         "keep_last": 7,
         "keep_daily": 30,
         "keep_weekly": 12,
         "keep_monthly": 24
       },
       "initialized": false,
       "created_at": "2026-04-05T12:00:00Z"
     }
   ]
   ```

2. **Add agent routes to `panel/agent/jabali-backup.php`:**
   - `jb.destinations_list` — Read destinations.json, return array. If file doesn't exist, synthesize one entry from config.conf [repository] for backward compat.
   - `jb.destinations_add` — Validate and append a new destination. Write to destinations.json.
   - `jb.destinations_update` — Update an existing destination by ID.
   - `jb.destinations_delete` — Remove a destination by ID (refuse if it's the only one).
   - `jb.destinations_test` — Run `restic cat config --repo <path>` to test connectivity/init status.
   - `jb.destinations_init` — Run `restic init` for a specific destination.
   - `jb.destinations_set_default` — Mark a destination as default (unset others).

3. **Credential files per destination:**
   - Credentials are stored as file references (e.g., `/etc/jabali-backup/dest-<id>-key`).
   - The `jb.destinations_add` route writes credential values to files atomically:
     1. Write to `/etc/jabali-backup/dest-<id>-key.tmp` with `file_put_contents()` + `0600` chmod.
     2. Verify file is readable: `is_readable()`.
     3. Rename `.tmp` → final path (atomic on same filesystem).
   - Never store raw secrets in destinations.json — only file paths.
   - `jb.destinations_delete` MUST also delete associated credential files for that destination ID.
   - File ownership: root:root (agent runs as root).

4. **Atomic JSON writes:**
   - All writes to destinations.json use write-to-tmp + rename pattern:
     ```php
     $tmp = $path . '.tmp';
     file_put_contents($tmp, json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES));
     chmod($tmp, 0600);
     rename($tmp, $path);
     ```
   - If destinations.json fails to parse (corrupted), return error — do NOT silently fall back, as this would hide data loss.

5. **Restic repo URI generation** (agent-side helper `jbBuildResticRepo()`):
   | type | Generated URI |
   |------|--------------|
   | local | `$path` (as-is) |
   | sftp | `sftp:$user@$host:$path` |
   | s3 | `s3:$endpoint/$bucket` |
   | b2 | `b2:$bucket:$path` |
   | gcs | `gs:$bucket:/$path` |
   | azure | `azure:$container:/$path` |
   | rest | `rest:$url` |
   | rclone | `rclone:$remote:$path` |
   | gdrive | `rclone:$remote:$path` |

6. **Backward compatibility in CLI:**
   - In `lib/config.sh`, add a `cfg_load_destination()` function that:
     - Reads `/etc/jabali-backup/destinations.json` if it exists.
     - Finds the default destination.
     - Sets `CFG_REPO_TYPE` and `CFG_REPO_PATH` from it.
     - Falls back to `config.conf [repository]` if no destinations.json.
   - Call `cfg_load_destination` from `cfg_load` after current loading.

### Verification
```bash
# Test agent route
printf '{"action":"jb.destinations_list","params":{}}\n' | timeout 5 socat -t5 - UNIX-CONNECT:/run/jabali/agent.sock
# Should return array with at least the legacy destination
```

### Exit Criteria
- destinations.json CRUD routes work via agent socket
- Backward compat: if destinations.json doesn't exist, jb.destinations_list returns synthesized entry from config.conf (in-memory only, does NOT auto-create the file)
- Credential files written with 0600 root:root permissions
- Credential files cleaned up on destination delete
- Corrupted destinations.json returns error, not silent fallback
- Atomic writes: no partial JSON on crash
- CLI still works with old single-repo config

### Rollback
Delete new routes from jabali-backup.php, remove destinations.json and any dest-*-key files.

---

## Step 2: Filament Destination Table + Add Destination Modal

**Branch:** `feat/multi-destination-ui`
**Depends on:** Step 1

### Context Brief
The agent now has CRUD routes for destinations. The Filament admin page (`panel/filament/pages/Backups.php`) needs to replace the single-destination info card with a table showing all destinations, and an "Add Destination" header action with a type-aware form modal.

### Tasks

1. **Replace `destinationInfo` state with `destinations` array:**
   - `public array $destinations = [];`
   - `loadDestinations()` calls `jb.destinations_list`

2. **Destination table in Blade view:**
   Replace the entire destination section with a table rendered from `$destinations`:
   - Columns: Name, Type (badge), Path/URL, Status (Initialized/Not Initialized badge), Size
   - Row actions: Test Connection, Edit, Set as Default, Delete (with confirmation)

3. **Add Destination Action (Filament modal form):**
   `addDestinationAction()` returns an `Action` with a multi-step form:
   - **Step 1 — Type selection:**
     ```
     Select::make('type')->options([
         'local'  => 'Local Directory',
         'sftp'   => 'SFTP / SSH',
         's3'     => 'Amazon S3 / Wasabi / MinIO',
         'b2'     => 'Backblaze B2',
         'gcs'    => 'Google Cloud Storage',
         'gdrive' => 'Google Drive (via rclone)',
         'azure'  => 'Azure Blob Storage',
         'rest'   => 'REST Server',
         'rclone' => 'Rclone Remote',
     ])
     ```
   - **Step 2 — Type-specific fields (conditionally visible):**
     - **Local:** `path` (TextInput)
     - **SFTP:** `host`, `port` (default 22), `user`, `path`, `ssh_key_file` (optional)
     - **S3:** `endpoint` (default s3.amazonaws.com), `bucket`, `access_key_id`, `secret_access_key`, `region`
     - **B2:** `bucket`, `account_id`, `account_key`
     - **GCS:** `bucket`, `project_id`, `credentials_json`
     - **Google Drive:** `rclone_remote_name`
     - **Azure:** `container`, `account_name`, `account_key`
     - **REST:** `url`, `username` (optional), `password` (optional)
     - **Rclone:** `remote_name`, `path`
   - **Common fields:** `name` (TextInput, required), retention toggles (keep_last, keep_daily etc. with defaults)

4. **Edit Destination Action:**
   Same form as Add, pre-filled with existing values. Calls `jb.destinations_update`.

5. **Wire actions to agent routes:**
   - Add → `jb.destinations_add`
   - Edit → `jb.destinations_update`
   - Delete → `jb.destinations_delete`
   - Test → `jb.destinations_test`
   - Init → `jb.destinations_init`
   - Set Default → `jb.destinations_set_default`

### Backend type → restic path mapping

| UI Type | restic repo format | Notes |
|---------|-------------------|-------|
| local | `/path/to/repo` | Direct path |
| sftp | `sftp:user@host:/path` | SSH key or password |
| s3 | `s3:endpoint/bucket` | AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY |
| b2 | `b2:bucket:path` | B2_ACCOUNT_ID + B2_ACCOUNT_KEY |
| gcs | `gs:bucket:/path` | GOOGLE_APPLICATION_CREDENTIALS |
| gdrive | `rclone:gdrive:path` | Rclone config required |
| azure | `azure:container:path` | AZURE_ACCOUNT_NAME + AZURE_ACCOUNT_KEY |
| rest | `rest:https://user:pass@host/` | Basic auth in URL |
| rclone | `rclone:remote:path` | Rclone config required |

### Verification
```bash
# Deploy and test
scp panel/filament/pages/Backups.php root@10.0.3.13:/var/www/jabali/app/Filament/Admin/Pages/Backups.php
scp panel/views/backups.blade.php root@10.0.3.13:/var/www/jabali/resources/views/filament/admin/pages/backups.blade.php
ssh root@10.0.3.13 'systemctl restart php8.5-fpm'
# Open /jabali-admin/backups — should see destination table with legacy entry
# Click "Add Destination" — should show type-aware modal
```

### Exit Criteria
- Destination tab shows a table of all configured destinations
- "Add Destination" modal works for all supported types
- Edit/Delete/Test/Init row actions work
- Default destination marked with a star badge (color: `warning`)
- Legacy single-destination still appears if destinations.json doesn't exist
- Page initializes `$destinations` in `mount()` via `loadDestinations()` call

### Rollback
Revert Backups.php and backups.blade.php to previous versions.

---

## Step 3: CLI Multi-Destination Support

**Branch:** `feat/multi-destination-cli`
**Depends on:** Step 1

### Context Brief
The CLI (`bin/jabali-backup`) needs to support `--destination=<name>` flag and read from destinations.json for the restic environment setup.

### Tasks

1. **Update `lib/config.sh`:**
   - Add `cfg_load_destination()`:
     - If `/etc/jabali-backup/destinations.json` exists:
       - Parse with `jq` (add jq as a dependency in doctor check).
       - If `--destination=<name>` was passed, find that destination.
       - Otherwise, find the one with `is_default: true`.
       - Set `CFG_REPO_TYPE`, `CFG_REPO_PATH`, and credential env vars from it.
     - Else: fall back to current config.conf parsing.

2. **Update `lib/restic.sh` `restic_env()`:**
   - Support reading credential file paths from the destination JSON entry.
   - The function already handles s3/b2/sftp/etc. Just need to source the credential file paths from the destination instead of hardcoded config keys.

3. **Update `bin/jabali-backup`:**
   - Add `--destination=<name>` global option.
   - Pass it through to `cfg_load_destination()`.
   - `jabali-backup run --destination="My S3"` backs up to that specific destination.

4. **Update `jabali-backup doctor`:**
   - Check that `jq` is installed.
   - Validate all destinations in destinations.json are reachable.

### Verification
```bash
jabali-backup doctor  # Should check jq and all destinations
jabali-backup run shuki --destination="Local Backup"  # Should use specific destination
jabali-backup run shuki  # Should use default destination
```

### Exit Criteria
- `--destination` flag works for all subcommands that touch restic
- Default destination used when flag omitted
- Backward compat: works without destinations.json
- `doctor` validates all destinations

### Rollback
Revert config.sh, restic.sh, bin/jabali-backup changes.

---

## Dependency Graph

```
Step 1 (Agent CRUD) ──┬──> Step 2 (Filament UI)
                      └──> Step 3 (CLI support)
```

Steps 2 and 3 are **parallel** — no shared file modifications.

## Summary

| Step | Description | Model | Serial/Parallel |
|------|------------|-------|-----------------|
| 1 | Agent CRUD routes + destinations.json | default | serial (foundation) |
| 2 | Filament table + Add Destination modal | default | parallel with 3 |
| 3 | CLI --destination flag | default | parallel with 2 |

**Total steps:** 3
**Parallelizable:** Steps 2 + 3 after Step 1
