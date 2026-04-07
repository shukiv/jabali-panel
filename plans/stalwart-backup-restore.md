# Blueprint: Stalwart Mail Server Backup & Restore

**Objective:** Complete the Stalwart mail server backup and restore integration — from the initial scaffolding (done) to production-ready with validation, error handling, panel UI, and cross-server migration support.

**Status:** Steps 1–5 are done. Steps 6–9 remain.

---

## Current State

We have a working skeleton:
- `lib/collectors/stalwart.sh` — exports accounts via `stalwart-cli export account`, falls back to REST API for principal metadata
- `lib/restorers/stalwart.sh` — imports via `stalwart-cli import account`, creates/updates principals via REST API
- `[stalwart]` config section — `enabled`, `url`, `admin_token_file`
- Wired into `cmd_run` and `cmd_restore` in `bin/jabali-backup`
- Disabled by default (`enabled=false`)

**Gaps identified:**
1. No connection/auth validation in `doctor` or `config test`
2. No dry-run support in either collector or restorer
3. Collector silently succeeds when Stalwart is down (curl fails → fallback fails → "failed" warning, continues)
4. Restorer uses `stalwart-cli import account` which doesn't exist — the correct command is `stalwart-cli import account <ACCOUNT> <PATH>` (same as export but reverse)
5. No overlap handling between `email` collector (Jabali DB + maildir) and `stalwart` collector (JMAP export) — both run by default
6. No panel UI for Stalwart config (enable/disable, test connection, token management)
7. No installer auto-detection of Stalwart or auto-configuration
8. No `stalwart` entry in the inventory check for the restore wizard
9. No cross-server migration documentation

---

## Dependency Graph

```
Step 3 ──┐
Step 4 ──┤
Step 5 ──┼── all independent, can run in parallel
Step 6 ──┤
Step 7 ──┘
         │
Step 8 ──┤── depends on Steps 3–7
Step 9 ──┘── depends on Step 8
```

---

## Step 1: Collector & Restorer Scaffolding ✅ DONE

Created `lib/collectors/stalwart.sh` and `lib/restorers/stalwart.sh`.

---

## Step 2: Config & CLI Wiring ✅ DONE

Added `[stalwart]` config section, wired collector/restorer into `cmd_run`/`cmd_restore`, added to completions and `--only`/`--exclude` flags.

---

## Step 3: Stalwart Doctor & Config Validation ✅ DONE

**Goal:** `jabali-backup doctor` and `jabali-backup config test` validate Stalwart connectivity when enabled.

### Context

`cmd_doctor` checks binary availability. `cmd_config` runs `config test` which validates DB connection and repo access. Neither currently checks Stalwart.

### Files to modify

- `bin/jabali-backup` — `cmd_doctor()` and `cmd_config()` functions

### Tasks

1. In `cmd_doctor()`, add a Stalwart section after the optional deps:
   - Check `stalwart-cli` availability (already done — verify)
   - If `[stalwart] enabled=true`, show the configured URL
   - If enabled but `admin_token_file` is missing/empty, warn

2. In `cmd_config()` (`config test` subcommand), add:
   - If Stalwart is enabled, `curl -sfS -H "Authorization: Bearer $token" "$url/api/principal?limit=1"` to verify auth
   - Print `✓ Stalwart: connected ($url)` or `✗ Stalwart: connection failed`

### Verification

```bash
# With Stalwart enabled + correct token:
sudo jabali-backup doctor       # shows stalwart-cli status + URL
sudo jabali-backup config test  # shows "Stalwart: connected"

# With bad token:
sudo jabali-backup config test  # shows "Stalwart: connection failed"

# With Stalwart disabled:
sudo jabali-backup config test  # no Stalwart section
```

### Exit criteria

- `config test` fails fast with clear error when Stalwart is enabled but unreachable
- `doctor` shows stalwart-cli and Stalwart URL when enabled

---

## Step 4: Fix Collector & Restorer Bugs ✅ DONE

**Goal:** Fix the bugs identified in the gap analysis.

### Files to modify

- `lib/collectors/stalwart.sh`
- `lib/restorers/stalwart.sh`

### Tasks

1. **Collector: validate Stalwart is reachable before exporting.** At the start of `collect_stalwart()`, ping the API (`GET /api/principal?limit=1`). If it fails, log an error and return 1 (not 0 — a soft fail should still be logged).

2. **Collector: add `--dry-run` support.** Check if `JABALI_DRY_RUN` is set. If so, log what would be exported but don't call `stalwart-cli` or curl.

3. **Restorer: fix the `import account` invocation.** The current code calls:
   ```bash
   stalwart-cli import account "$account" "$source_dir"
   ```
   Verify this matches the actual `stalwart-cli import account` usage from docs: `stalwart-cli -u <URL> import account <ACCOUNT> <PATH>`. The current code passes `-u` and `-c` correctly via `cli_args`, so this just needs testing.

4. **Restorer: add `--dry-run` support.** If `JABALI_DRY_RUN` is set, log what would be imported but don't call API or CLI.

5. **Restorer: add `--force` handling for JMAP import.** Currently `_stalwart_import_cli` doesn't check the force flag — it always imports. Document the behavior: JMAP import is additive (doesn't delete existing emails), so `--force` only applies to principal recreation.

### Verification

```bash
sudo jabali-backup run alice --only=stalwart --dry-run
# Should log "Would export: alice@example.com" without calling Stalwart

sudo jabali-backup restore alice --only=stalwart --dry-run
# Should log "Would import: alice@example.com" without calling Stalwart
```

### Exit criteria

- Collector returns non-zero when Stalwart is unreachable (not silent success)
- Dry-run works for both collector and restorer
- Import command matches stalwart-cli docs

---

## Step 5: Email vs Stalwart Collector Deduplication ✅ DONE

**Goal:** Prevent backing up the same mail data twice — once via `email` (Jabali DB + maildir paths) and again via `stalwart` (JMAP export).

### Context

The `email` collector captures:
- Email domain config from Jabali DB (DKIM, catch-all, quotas)
- Mailbox records from Jabali DB (local_part, domain, protocol flags)
- Maildir paths added to restic backup (raw mail files)
- Forwarders, autoresponders, shares from Jabali DB

The `stalwart` collector captures:
- Full JMAP account export (emails, mailbox structure, Sieve, identities, vacation)
- Stalwart principal metadata

**Overlap:** Mail content is in both (maildir files via restic, and JMAP email export via stalwart-cli). No overlap on Jabali DB metadata (email collector handles that exclusively).

### Files to modify

- `lib/collectors/email.sh` — skip maildir path collection when `stalwart` is also enabled
- `bin/jabali-backup` — `cmd_run()` dry-run output

### Tasks

1. In `collect_email()`, after collecting mailbox records, check `CFG_STALWART_ENABLED`. If `true`, skip adding maildir paths to `mail_paths` array and log: `"email: Skipping maildir paths (handled by stalwart collector)"`. The Jabali DB metadata (domains, mailboxes, forwarders, autoresponders, shares) is still collected — only the raw maildir files are skipped.

2. Add a comment in `collect_stalwart()` explaining the relationship: email collector handles Jabali DB metadata, stalwart collector handles mail content and JMAP state.

3. In dry-run output, show: `"stalwart: alice@example.com (JMAP export — replaces maildir)"`.

### Verification

```bash
# With stalwart enabled:
sudo jabali-backup run alice --dry-run
# email collector shows domains/mailboxes/forwarders but NOT maildir paths
# stalwart collector shows JMAP export

# With stalwart disabled (default):
sudo jabali-backup run alice --dry-run
# email collector shows everything including maildir paths
```

### Exit criteria

- No duplicate mail content in backup when both collectors are enabled
- Jabali DB metadata always collected by email collector regardless of stalwart setting

---

## Step 6: Restore Wizard Inventory

**Goal:** The panel's restore wizard shows Stalwart data as a selectable component.

### Context

The restore wizard in `Backups.php` calls `jb.snapshot_inventory` to discover what's in a snapshot. The inventory drives which tabs appear in the wizard (files, databases, email, DNS, etc.). Stalwart is not in the inventory yet.

### Files to modify

- `panel/agent/jabali-backup.php` — `jbSnapshotInventory()` handler
- `panel/filament/pages/Backups.php` — restore wizard steps

### Tasks

1. In `jbSnapshotInventory()`, check for `stalwart/` directory in the extracted snapshot. Return:
   ```json
   "stalwart": {
     "exists": true,
     "accounts": ["alice@example.com", "alice@other.com"]
   }
   ```

2. In `Backups.php` restore wizard `->steps()` closure, add a Stalwart tab after the Email tab:
   ```php
   if (($inv['stalwart']['exists'] ?? false) && ! empty($inv['stalwart']['accounts'])) {
       $accounts = collect($inv['stalwart']['accounts'])->mapWithKeys(fn ($a) => [$a => $a])->all();
       $tabs[] = Tab::make(__('Stalwart Mail'))
           ->icon('heroicon-o-inbox-stack')
           ->schema([
               CheckboxList::make('restore_stalwart')
                   ->label(__('JMAP accounts to restore'))
                   ->options($accounts)
                   ->default(array_keys($accounts)),
           ]);
   }
   ```

3. In the wizard's `->action()` handler, include `stalwart` in components when `restore_stalwart` is non-empty.

### Verification

- Restore wizard shows a "Stalwart Mail" tab when snapshot contains stalwart data
- Deselecting accounts skips them during restore
- Summary step shows selected Stalwart accounts

### Exit criteria

- Restore wizard correctly discovers and displays Stalwart data from snapshots
- User can selectively restore individual Stalwart accounts

---

## Step 7: Installer Auto-Detection

**Goal:** `install.sh` auto-detects Stalwart and pre-configures the `[stalwart]` section.

### Files to modify

- `install.sh`

### Tasks

1. After the config generation block (around line 320), add Stalwart detection:
   ```bash
   # Detect Stalwart
   if command -v stalwart-cli &>/dev/null || [[ -d /opt/stalwart-mail ]]; then
       info "Stalwart mail server detected"
       if [[ ! -f "$INSTALL_ETC/stalwart-token" ]]; then
           warn "Set Stalwart admin token: echo 'TOKEN' | sudo tee $INSTALL_ETC/stalwart-token"
       fi
       # Enable in config if not already set
       if ! grep -q '^\[stalwart\]' "$INSTALL_ETC/config.conf" 2>/dev/null; then
           cat >> "$INSTALL_ETC/config.conf" <<STALWART
   
   [stalwart]
   enabled=false
   url=http://localhost:8080
   admin_token_file=$INSTALL_ETC/stalwart-token
   STALWART
           ok "Added [stalwart] section to config (disabled — set token and enable)"
       fi
   fi
   ```

2. Add `stalwart-cli` to the optional dependency check (not auto-installed — it's part of the Stalwart package).

### Verification

```bash
# On a server with Stalwart installed:
sudo ./install.sh
# Shows "Stalwart mail server detected"
# Adds [stalwart] section to config.conf
# Warns about setting the admin token

# On a server without Stalwart:
sudo ./install.sh
# No stalwart-related output
```

### Exit criteria

- Installer detects Stalwart and adds config section
- Does NOT auto-enable (user must set token and enable manually)

---

## Step 8: Panel Stalwart Config UI

**Goal:** Admin can enable/disable Stalwart backup and test the connection from the panel.

### Files to modify

- `panel/agent/jabali-backup.php` — add `jb.stalwart_status` and `jb.stalwart_test` routes
- `panel/filament/pages/Backups.php` — settings tab

### Tasks

1. Add agent route `jb.stalwart_status`:
   - Read `[stalwart]` section from config
   - Return `{enabled, url, has_token, cli_available}`

2. Add agent route `jb.stalwart_test`:
   - Call `GET /api/principal?limit=1` with the configured token
   - Return `{success, message, version?}`

3. Add agent route `jb.stalwart_toggle`:
   - Write `enabled=true` or `enabled=false` to config
   - Validate token file exists before enabling

4. In `Backups.php` settings tab, add a "Stalwart Mail Backup" section:
   - Toggle to enable/disable
   - "Test Connection" button
   - Status indicator (connected/disconnected/not configured)
   - Link to docs for setting up the admin token

### Verification

- Settings tab shows Stalwart section
- Test Connection button works
- Toggle enable/disable persists to config

### Exit criteria

- Admin can manage Stalwart backup from the panel without SSH

---

## Step 9: Cross-Server Migration Documentation

**Goal:** Document how to use the Stalwart backup for migrating between servers.

### Files to modify

- `docs/RESTORE.md` — add Stalwart section

### Tasks

1. Add "Stalwart Account Migration" section to RESTORE.md covering:
   - Same-server restore (account recovery)
   - Cross-server migration (export on source, import on target)
   - What gets migrated (emails, folders, Sieve scripts, identities, vacation)
   - What does NOT get migrated (passwords — must be reset)
   - APP_KEY is not needed (Stalwart manages its own encryption)
   - Example workflow:
     ```bash
     # Source server
     sudo jabali-backup run alice --only=stalwart
     
     # Copy snapshot to target (or use shared destination)
     
     # Target server
     sudo jabali-backup restore alice --only=stalwart --force
     ```

### Verification

- Docs are accurate and match implementation
- Example commands work

### Exit criteria

- A sysadmin can follow the docs to migrate an account between two Jabali servers

---

## Rollback

Each step is independent (except 8→9). Rollback for any step:
- Revert the commit
- Stalwart backup remains opt-in (`enabled=false` by default), so partial implementation doesn't affect existing users

## Model Routing

| Step | Model | Rationale |
|------|-------|-----------|
| 3 | Default | Simple validation logic |
| 4 | Default | Bug fixes in existing code |
| 5 | Default | Small conditional in one file |
| 6 | Default | Follows established wizard pattern |
| 7 | Default | Simple bash detection logic |
| 8 | Strongest | Panel UI + agent routes + config write — needs careful Filament v5 patterns |
| 9 | Default | Documentation only |
