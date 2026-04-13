# Blueprint: Granular Restore Modal

## Objective

When clicking "Restore" on an account row, open a full-page modal with tabs for each backup component. Each tab shows the actual items in the snapshot (specific databases, domains, files, etc.) and lets the admin select exactly what to restore.

## Current State

Restore modal shows a flat checkbox list of component categories (files, databases, email, etc.) — all-or-nothing per category.

## Target State

Restore modal shows **tabbed component browser** with selectable items:

| Tab | Shows | Selectable items |
|-----|-------|------------------|
| Panel Config | account.json summary | Checkbox: restore user metadata |
| Home Dir Files | File tree (top-level dirs) | Checkboxes per directory/file |
| Databases | List of .sql.gz files | Checkboxes per database |
| Email | Email domains + mailboxes | Checkboxes per email domain |
| Domains | Domain list from account.json | Checkboxes per domain |
| Cron Jobs | Cron entries | Checkbox: restore crons |
| SSL Certificates | Cert list per domain | Checkboxes per domain cert |
| Nginx Config | Config files | Checkboxes per vhost config |
| PHP Config | Pool configs | Checkbox: restore PHP pool |

## Data Source

All data comes from inspecting the snapshot via restic. One new agent RPC:

```
jb.snapshot_inventory → {
  username: "shuki",
  snapshot_id: "f8ef12d8",
  components: {
    metadata: { exists: true, user: {...}, domains: [...] },
    files: { exists: true, top_dirs: ["domains", "mail", "ssl", "cache", ...] },
    mysql: { exists: true, databases: ["shuki_wp"], grants: true },
    dns: { exists: true, zones: ["123123.com"] },
    email: { exists: true, domains: ["123123.com"], mailboxes: [] },
    nginx: { exists: true, configs: ["123123.com.conf"] },
    php: { exists: true, pools: ["shuki.conf"], version: "8.5" },
    ssl: { exists: true, domains: [] },
    cron: { exists: true, jobs: [] },
  }
}
```

This is a single `restic ls` + `restic dump` of account.json — fast, no extraction needed.

## Steps

### Step 1: Agent RPC — `jb.snapshot_inventory`

**File:** `panel/agent/jabali-backup.php`

New function `jbSnapshotInventory($params)`:
- Input: `username`, `snapshot_id` (default: latest)
- Runs `restic ls --tag account:{username} {snapshot_id}` to get file listing
- Runs `restic dump` on `account.json` for metadata
- Parses the file listing to extract:
  - `mysql/*.sql.gz` → database names (strip .sql.gz)
  - `dns/*.json` → zone names (strip .json)
  - `email/domains/*.json` → email domain names
  - `nginx/*.conf` → config names
  - `php/*.conf` → pool names
  - `ssl/*/` → domain names with certs
  - `cron/*` → cron job files
  - Home dir top-level directories
- Returns structured inventory

**Exit criteria:** `AgentClient->send('jb.snapshot_inventory', [...])` returns component lists.

### Step 2: Filament Restore Modal with Tabs

**File:** `panel/filament/pages/Backups.php`

Replace the current flat `->form([...])` on the restore action with a tabbed modal:

```php
->form(function (array $record): array {
    // Call agent to get inventory
    $inventory = app(AgentClient::class)->send('jb.snapshot_inventory', [
        'username' => $record['username'],
        'snapshot_id' => $record['latest_snapshot_id'],
    ]);
    
    return [
        Select::make('snapshot')->...,
        Tabs::make('components')->tabs([
            Tab::make('Databases')->schema([
                CheckboxList::make('databases')
                    ->options($inventory['mysql']['databases'] ?? [])
            ]),
            Tab::make('Domains')->schema([
                CheckboxList::make('domains')
                    ->options($inventory['metadata']['domains'] ?? [])
            ]),
            // ... etc for each component
        ]),
        Toggle::make('force'),
    ];
})
```

**Exit criteria:** Clicking Restore opens tabbed modal with actual snapshot contents.

### Step 3: CLI Granular Restore Flags

**File:** `bin/jabali-backup`

Extend `cmd_restore` to accept granular selection:
- `--databases=db1,db2` — restore only these databases
- `--domains=example.com` — restore only these domains
- `--files=domains/,mail/` — restore only these directories

The restorer scripts already exist — just need to pass the selection filter to them.

**Exit criteria:** CLI can restore specific items within components.

### Step 4: Wire Panel Selection to CLI

**File:** `panel/agent/jabali-backup.php`, `panel/filament/pages/Backups.php`

The `jb.restore` handler passes granular selections from the modal to the CLI:
```php
$args[] = '--databases=' . implode(',', $data['databases']);
$args[] = '--domains=' . implode(',', $data['domains']);
```

**Exit criteria:** Panel granular selections result in selective CLI restore.

## Dependency Order

```
Step 1 (inventory RPC) → Step 2 (modal UI)
                        → Step 3 (CLI flags) → Step 4 (wiring)
```

Steps 2 and 3 can be done in parallel after Step 1.

## What NOT to change

- The actual restorer scripts (they already handle per-item restore)
- The restic integration
- The download mechanism
