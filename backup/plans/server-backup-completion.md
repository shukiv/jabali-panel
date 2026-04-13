# Plan: Server Backup + Disaster Restore — Completion & Audit

> **Objective.** The full server (disaster recovery) backup/restore feature was
> drafted in `server-backup-disaster-restore.md` and most of the scaffolding has
> shipped. This plan audits the implementation against
> [`/home/shuki/projects/jabali/docs/backup-server-spec.md`](../../jabali/docs/backup-server-spec.md),
> closes the remaining spec gaps, and makes sure every DR capability in the
> panel has a matching CLI path. Every UI label explicitly uses
> **"Disaster Recovery"** so operators never confuse it with per-user backup.
>
> **Source of truth.** The panel team's spec is authoritative. Where the spec
> says "preferred restore via install.sh + git clone", we keep that path and
> back up files only as fallback metadata. Where the spec says "must be
> encrypted", we rely on restic's built-in encryption (every snapshot is
> encrypted with `/etc/jabali/restic-password`) — no extra layer is added.
>
> **Scope boundary.** Per `feedback_no_touch_jabali.md`, this plan does NOT
> modify the `jabali` repo. All changes live in `jabali-backup/` (CLI, agent
> addon, Filament page, tests, docs). The `backup-server-spec.md` reference is
> read-only.

---

## Current State (what already ships on `main` as of `11fd5c4`)

| Area | Status | Location |
|------|--------|----------|
| Collector entry (`collect_server`) | ✅ | `lib/collectors/server.sh:5` |
| Restorer entry + 5 phases (`restore_server`) | ✅ | `lib/restorers/server.sh:5` |
| CLI `server-backup` command | ✅ | `bin/jabali-backup:757` (`cmd_server_backup`) |
| CLI `server-restore` command | ✅ | `bin/jabali-backup:866` (`cmd_server_restore`) |
| CLI schedule `--server-backup` flag | ✅ | `bin/jabali-backup:3086,3169` |
| Schedule cron builder picks server-backup | ✅ | `bin/jabali-backup:2933` (`_sched_build_cron_cmd`) |
| Agent RPC `jb.server_backup` / `jb.server_restore` | ✅ | `panel/agent/jabali-backup.php:59,61` |
| Agent RPC `jb.server_download` via pipe/tempfile | ✅ | `jbServerDownloadPipe` (ADR-0005) |
| Panel Disaster-Recovery action group | ✅ | `Backups.php:220-283` |
| Panel per-row server-restore / server-download / browse / delete | ✅ | `Backups.php:962,1023,1034` |
| Schedule form toggle "Server Backup (Disaster Recovery)" | ✅ | `Backups.php:1382-1386` |
| `runScheduleNow` routes to `jb.server_backup` | ✅ | `Backups.php:1654` |
| Snapshot inventory shows `__server__` row with badge | ✅ | `Backups.php:335,619-622` |
| Server snapshot tags `type:server,date:...,hostname:...` | ✅ | `cmd_server_backup` (line 824) |
| `server-manifest.json` generated | ✅ | `_generate_server_manifest` (`server.sh:305`) |

**Spec categories 1–20 coverage as currently implemented:**

| # | Spec category | Covered? | Notes |
|---|---------------|----------|-------|
| 1 | Panel DB `jabali` | ✅ | Ephemeral-table skip list matches spec |
| 2 | PowerDNS DB + DNSSEC state | ✅ | `pdnsutil show-zone` per zone |
| 3 | Panel application `/var/www/jabali/` | ⚠️ partial | Only `storage/app`, composer files, git-info captured. Restore assumes `git clone` — breaks if installed without `.git` |
| 4 | Panel `.env` | ✅ | Copied 0600 |
| 5 | `/etc/jabali/` | ✅ | Whole dir, includes `agent.d/`, templates, `restic-password`, `stalwart-api.conf` |
| 6 | Nginx config | ✅ | Includes `jabali/`, vhosts, enabled symlinks list |
| 7 | PHP-FPM | ✅ | Per PHP version |
| 8 | FrankenPHP / Caddyfile | ✅ | Inside `/etc/jabali/Caddyfile` (captured via #5) |
| 9 | Panel SSL `/etc/ssl/jabali/` | ✅ | |
| 10 | Stalwart config | ⚠️ partial | `/etc/stalwart-mail/` only. **Missing `/var/lib/stalwart-mail/` (DKIM keys + mail data)** |
| 11 | Redis config | ✅ | ACL passwords preserved in `redis.conf` |
| 11 | Redis data (optional) | ❌ | `/var/lib/redis/dump.rdb` not collected. Spec explicitly calls it optional — document or opt in |
| 12 | MariaDB `/etc/mysql/` | ✅ | Including `debian.cnf` (0600) |
| 13 | Systemd units | ✅ | `jabali-*`, slice, stalwart, bulwark, nginx override |
| 14 | Restic password | ✅ | Inside `/etc/jabali/` (via #5) |
| 15 | Agent addons | ✅ | Inside `/etc/jabali/agent.d/` (via #5) |
| 16 | Custom templates | ✅ | Inside `/etc/jabali/` (via #5) |
| 17 | Let's Encrypt | ✅ | Full `/etc/letsencrypt/` including `accounts/` |
| 18 | Bulwark `/opt/bulwark/` | ❌ | Not collected. Spec's preferred restore = reinstall via `install.sh`; we should skip files but record intent in manifest |
| 19 | All user accounts | ✅ | Phase 4 calls per-user restore for each account |
| 20 | Package manifest | ✅ | `dpkg --get-selections`, `apt-mark showmanual`, versions |

**The open gaps are therefore small and targeted.** The rest of this plan drives
them to zero and adds the missing CLI surface + tests.

---

## Gap List (what this plan closes)

| ID | Gap | Severity | Step |
|----|-----|----------|------|
| G1 | Stalwart **data** (`/var/lib/stalwart-mail/`) not collected — DKIM keys lost on restore | **HIGH** | Step 1 |
| G2 | Bulwark collection absent + restore phase doesn't reinstall | MEDIUM | Step 2 |
| G3 | Panel app source fallback: if `.git` missing, restore can't rehydrate | MEDIUM | Step 3 |
| G4 | CLI has no `server-download` subcommand — panel can download, CLI can't | MEDIUM | Step 4 |
| G5 | CLI `ls __server__` / `list server-snapshots` not smoke-tested | LOW | Step 5 |
| G6 | No bash integration test for `server-backup → server-restore` round trip | MEDIUM | Step 6 |
| G7 | `docs/README.md` + `docs/ARCHITECTURE.md` don't enumerate the new server flags | LOW | Step 7 |

Model-tier assignment: all steps are mechanical edits except Step 6 (integration
harness design) — use **Sonnet (default)** throughout; Opus not required.

Each step is **one PR, one logical concern**. Steps 1 & 2 are parallel
(independent collector+restorer slices). Steps 3, 4 are parallel to each other.
Steps 5 & 6 depend on 1–4 landing. Step 7 is last.

```
Step 1 (G1) ──┐
Step 2 (G2) ──┤
Step 3 (G3) ──┼──► Step 5 (G5) ──► Step 6 (G6) ──► Step 7 (G7)
Step 4 (G4) ──┘
```

---

## Step 1 — Collect & restore Stalwart mail data (DKIM, mailboxes)

**Gap:** G1. **Branch:** `feat/server-stalwart-data`
**Files:** `lib/collectors/server.sh`, `lib/restorers/server.sh`, `lib/collectors/stalwart.sh` (reference)
**Depends on:** nothing.

### Context brief (cold-start)

Stalwart stores DKIM private keys and mail data under `/var/lib/stalwart-mail/`
(RocksDB by default). Losing this directory means every domain's DKIM signature
breaks on restore — users see mail rejected as "DKIM: none" until keys are
regenerated and DNS is updated. Spec §10 flags this as critical.

`_collect_server_stalwart` at `lib/collectors/server.sh:189` currently copies
only `/etc/stalwart-mail/`. We need to additionally capture `/var/lib/stalwart-mail/`
(or, if Stalwart is running, use `stalwart-cli server shutdown && rsync && start`
to avoid hot-copying RocksDB — see `lib/collectors/stalwart.sh` for how per-user
mail export already handles this).

The cleanest approach: stop the service, rsync, restart. Document the short
(<5 s) service interruption in the server backup banner.

### Tasks

1. In `lib/collectors/server.sh::_collect_server_stalwart`, after copying
   `/etc/stalwart-mail/`, add an opt-out-able block that:
   - Checks `[[ "${CFG_SERVER_BACKUP_STALWART_DATA:-true}" == "true" ]]`.
   - Stops `stalwart-mail.service` (grace period 10 s).
   - `rsync -a --numeric-ids /var/lib/stalwart-mail/ "$dest_data/"` where
     `dest_data="${server_dir}/data/stalwart"`.
   - Restarts `stalwart-mail.service`.
   - Uses a trap (`trap ... ERR RETURN`) to guarantee restart even on failure.
2. In `lib/restorers/server.sh::_restore_server_phase3` (Stalwart block around
   line 159), after restoring `/etc/stalwart-mail/`, if
   `${server_dir}/data/stalwart/` exists:
   - Stop `stalwart-mail.service`.
   - `rsync -a --numeric-ids` the data back to `/var/lib/stalwart-mail/`.
   - `chown -R stalwart-mail:stalwart-mail /var/lib/stalwart-mail` (verify user/group name via `id stalwart-mail 2>/dev/null`).
   - Restart service.
3. **Restart safety (critical).** Both collector and restorer must verify
   Stalwart actually came back up. Do not rely on the trap alone:
   ```bash
   systemctl start stalwart-mail
   for i in {1..30}; do
     systemctl is-active --quiet stalwart-mail && break
     sleep 0.5
   done
   if ! systemctl is-active --quiet stalwart-mail; then
     log_error "CRITICAL: stalwart-mail failed to restart after data capture"
     return 2   # fail collect_server loudly; restorer must abort phase
   fi
   ```
4. Update `_generate_server_manifest` to add
   `"components.stalwart_data": true|false` based on whether data was captured.
   Also confirm `_generate_server_manifest` is actually called at the tail of
   `collect_server` (today's code does so at `server.sh:24`; don't silently
   drop the call when editing).
5. Add a CLI flag `--skip-stalwart-data` to `cmd_server_backup` that sets
   `CFG_SERVER_BACKUP_STALWART_DATA=false` for that run.

### Verification

```bash
# On a box with Stalwart running:
sudo jabali-backup server-backup
# Inspect the restic snapshot's data/stalwart directory
sudo jabali-backup ls __server__ data/stalwart | head

# Restore onto a scratch VM, then confirm DKIM signature matches
sudo jabali-backup server-restore --snapshot=latest
sudo stalwart-cli --url http://localhost:8080 dkim-keys list | grep example.com
```

### Exit criteria

- [ ] `data/stalwart/` appears in server snapshots with at least one file.
- [ ] Manifest records `stalwart_data: true`.
- [ ] Post-restore, `stalwart-mail.service` reaches `active (running)` and the
      public DKIM strings from `stalwart-cli dkim-keys list <domain>` match
      byte-for-byte between pre-backup and post-restore (verify via text diff
      of the "Public" lines, not the RocksDB blob — LSM compaction may
      re-arrange bytes without changing key material).
- [ ] Stalwart downtime during backup < 10 s on an idle box.
- [ ] `--skip-stalwart-data` produces a snapshot without `data/stalwart/`.
- [ ] `bash -n lib/collectors/server.sh lib/restorers/server.sh` clean.

---

## Step 2 — Bulwark: record install intent, skip files

**Gap:** G2. **Branch:** `feat/server-bulwark-metadata`
**Files:** `lib/collectors/server.sh`, `lib/restorers/server.sh`
**Depends on:** nothing. Parallel to Step 1.

### Context brief

Spec §18 says **preferred restore is reinstall via `install.sh`'s Bulwark
section**, because copying built Next.js files between dissimilar hosts is
fragile. We adopt that recommendation: don't copy `/opt/bulwark/`, but
**record** that Bulwark was installed so the restorer can reinvoke the
installer's Bulwark section. The existing `install.sh` already has a
`patch_bulwark()` path (see recent session work in `/tmp/bulwark-pr/`).

### Tasks

1. Add `_collect_server_bulwark` to `lib/collectors/server.sh`:
   - If `/opt/bulwark/` exists, capture `package.json` version, a recorded
     upstream git SHA (if `.git` present), and port (read from
     `/etc/systemd/system/bulwark.service`).
   - Write JSON to `${server_dir}/metadata/bulwark.json`.
   - Do **not** copy any files — keep the snapshot lean.
2. Call it from `collect_server` after `_collect_server_systemd`.
3. In `_restore_server_phase5`:
   - If `metadata/bulwark.json` exists **and** `/opt/bulwark/` does not, log a
     WARN instructing the operator to run
     `sudo /opt/jabali-backup/install.sh --reinstall-bulwark` (do not auto-run;
     Bulwark reinstall downloads from the internet and may surprise the
     operator).
   - **Backwards-compat for pre-Step-2 snapshots:** when `metadata/bulwark.json`
     is absent but `bulwark` command or `/opt/bulwark/` is detected on the
     restore target, log `WARN: legacy snapshot (no bulwark metadata); verify
     /opt/bulwark manually.` so operators aren't silently left in a half-state.
4. Update manifest `"components.bulwark": "metadata-only"|"missing"|"inline"`.

### Verification

```bash
sudo jabali-backup server-backup
sudo jabali-backup ls __server__ metadata/bulwark.json
cat /tmp/server-restore-check.log | grep Bulwark
```

### Exit criteria

- [ ] `metadata/bulwark.json` exists when Bulwark installed; absent otherwise.
- [ ] Restore log emits the reinstall hint exactly once.
- [ ] No file under `/opt/bulwark/` is in the restic snapshot.

---

## Step 3 — Panel-app source fallback (no-git recovery path)

**Gap:** G3. **Branch:** `feat/server-panel-app-fallback`
**Files:** `lib/collectors/server.sh`, `lib/restorers/server.sh`, `docs/ARCHITECTURE.md`
**Depends on:** nothing. Parallel to Step 1.

### Context brief

`_collect_server_panel` (server.sh:80) currently assumes `/var/www/jabali/.git`
exists and restore can `git clone` from `git-info.json`. If a production box was
deployed via tarball or the git directory was pruned, restore will fail with
"no app code". Spec §3 lists files to include as the non-git fallback:
`app/ config/ routes/ resources/ database/ bin/ stubs/ public/ storage/app/`.

### Tasks

1. In `_collect_server_panel`: when `.git` is absent, copy the fallback tree
   under `${panel_dir}/source/` (tar with `--exclude` for
   `vendor/ node_modules/ storage/framework/cache/ storage/framework/views/
   bootstrap/cache/ .git/ node_modules/`).
2. **Dirty-git safety.** When `.git` is present, additionally record the
   working-tree dirtiness so restore doesn't silently drop uncommitted work:
   ```bash
   git -C "$jabali_path" status --porcelain > "${panel_dir}/git-dirty.txt"
   git -C "$jabali_path" diff > "${panel_dir}/git-dirty.patch"
   ```
   Both files will be empty on a clean tree. Size-check (`[[ -s … ]]`) on
   restore to decide whether to warn.
3. Write `${panel_dir}/restore-mode.txt` = `git` | `tarball`.
4. In `_restore_server_phase1`, branch on `restore-mode.txt`:
   - `git`: existing behaviour (clone). If `git-dirty.txt` is non-empty, log
     `WARN: original tree had uncommitted changes — see git-dirty.patch inside
     the snapshot; re-apply manually after clone.`
   - `tarball`: mkdir `/var/www/jabali`, extract, run
     `composer install --no-dev --optimize-autoloader`, `npm ci && npm run build`,
     `php artisan migrate --force`, `php artisan storage:link`.
5. Preserve ownership: post-restore, look up the FPM pool user rather than
   hardcoding:
   ```bash
   fpm_user=$(grep -h '^user = ' /etc/php/*/fpm/pool.d/www.conf 2>/dev/null | awk '{print $3}' | head -1)
   fpm_user="${fpm_user:-www-data}"
   chown -R "${fpm_user}:${fpm_user}" /var/www/jabali
   ```

### Verification

```bash
# On a git-less test box:
sudo rm -rf /var/www/jabali/.git
sudo jabali-backup server-backup
# Restore on scratch:
sudo jabali-backup server-restore --snapshot=latest --target=/tmp/scratch
ls /tmp/scratch/var/www/jabali/app | head
```

### Exit criteria

- [ ] Snapshot from a git-less install contains `panel/source/` directory.
- [ ] Snapshot from a git-ful install has `git-info.json` and no `source/`.
- [ ] Restore decision logged as "panel restore mode: git" or "… tarball".

---

## Step 4 — CLI `server-download` subcommand

**Gap:** G4. **Branch:** `feat/cli-server-download`
**Files:** `bin/jabali-backup`, `docs/README.md`
**Depends on:** nothing. Parallel to Step 1.

### Context brief

Panel offers a "Download Server Backup" button (`Backups.php:1023`, routes to
`jb.server_download` via `jbServerDownloadPipe` → temp file + `.done` sentinel
approach — see ADR-0005). CLI parity is missing: `jabali-backup download alice`
works, `jabali-backup download __server__` is undefined behaviour.

Per user requirement: *"all this should be reflected in the cli"*. Add an
explicit `server-download` subcommand that produces a single tarball combining
the server snapshot and (optionally) every user snapshot from the same run —
matching what the panel builds.

### Tasks

1. Implement `cmd_server_download` in `bin/jabali-backup`:
   ```
   Usage: jabali-backup server-download [--snapshot=ID|latest]
                                         [--output=PATH]
                                         [--destination=NAME]
                                         [--configs-only]
   ```
   - `--configs-only` = server snapshot only, no per-user data (fast).
   - Without `--configs-only` = server + latest per-user snapshots combined,
     same as panel's "full server download".
2. Dispatch in the main `case` at line ~3346:
   `server-download) cmd_server_download "$@" ;;`.
3. Add to the help banner (after `server-restore`, line 55):
   `server-download    Download full server snapshot as tar.gz`.
4. Reuse the restic-restore-to-staging+tar pipeline that `jbServerDownloadPipe`
   uses; refactor common bits into a sourced helper
   (`lib/server-download.sh`) so panel and CLI agree.
   - **Scope note:** `panel/agent/jabali-backup.php` is inside this repo, so
     this step also edits `jbServerDownloadPipe` to call the new Bash helper
     via `proc_open` (or inline the minimal Bash snippet). Both paths go in
     the same PR — the panel addon cannot be stale relative to the CLI.
   - Do NOT edit anything under `/home/shuki/projects/jabali/` — the Jabali
     core repo stays untouched.

### Verification

```bash
sudo jabali-backup server-download --configs-only --output=/tmp/srv.tgz
tar -tzf /tmp/srv.tgz | head
sudo jabali-backup server-download --output=/tmp/full.tgz
tar -tzf /tmp/full.tgz | grep home/ | head
```

### Exit criteria

- [ ] `jabali-backup server-download --configs-only` produces a tarball ≈ size
      of the server snapshot (no `/home/*` entries).
- [ ] `jabali-backup server-download` produces a tarball containing `server/`
      and every active user's home tree.
- [ ] Panel's `jbServerDownloadPipe` and CLI go through the same helper
      (verify via `grep -n "server-download-build" panel/agent/jabali-backup.php bin/jabali-backup`).
- [ ] `jabali-backup --help` lists the new command.

---

## Step 5 — CLI browse/list parity for `__server__`

**Gap:** G5. **Branch:** `feat/cli-server-browse-parity`
**Files:** `bin/jabali-backup`
**Depends on:** Steps 1 & 4 landed (they affect snapshot contents/commands).

### Context brief

Panel allows browsing server snapshots (Backups.php browse action). CLI side:
`cmd_ls` already special-cases the server path via `restic snapshots --tag type:server`
(line 824ff) but the code path is only reachable when users guess
`jabali-backup ls __server__`. That's discoverable for the author, not for
operators. Add a first-class listing path.

### Tasks

1. Extend `cmd_list` (line 366) with:
   - `jabali-backup list server-snapshots [--json]` → list all `type:server`
     snapshots with hostname, date, size.
   - Update help text.
2. In `cmd_ls`, detect the sentinel `__server__` and also accept `server` as an
   alias (`jabali-backup ls server configs/jabali/agent.d` is ergonomic).
3. Print a one-line hint in `cmd_server_backup` when it completes:
   `Browse with: jabali-backup ls server`.

### Verification

```bash
sudo jabali-backup list server-snapshots
sudo jabali-backup list server-snapshots --json | jq '.[0]'
sudo jabali-backup ls server
sudo jabali-backup ls server config/jabali
```

### Exit criteria

- [ ] `list server-snapshots` prints a table with ≥ 1 row on a backed-up box.
- [ ] `ls server` returns the snapshot root contents.
- [ ] CLI help (`jabali-backup --help`) mentions both.

---

## Step 6 — Integration test: server backup → restore round trip

**Gap:** G6. **Branch:** `test/server-backup-roundtrip`
**Files:** `tests/bash/test_server_backup_roundtrip.sh` (new), `tests/run-tests.sh`
**Depends on:** Steps 1–4 landed.

### Context brief

`tests/bash/` has three existing tests (`test_backup_restore_coverage.sh`,
`test_installer_self_update.sh`, `test_staging_patterns.sh`). None exercises
the server path. This step adds a single smoke test that runs entirely inside a
scratch staging dir (no real Stalwart / nginx needed — mock via fixture).

### Tasks

1. New test `tests/bash/test_server_backup_roundtrip.sh`:
   - Sets `CFG_JABALI_PATH=/tmp/fake-jabali` and seeds a minimal tree
     (`.env`, `composer.json`, fake `storage/app/logo.png`, `.git/`).
   - Seeds `/tmp/fake-etc/jabali/{restic-password,welcome.html,agent.d/}`.
   - Monkey-patches system tools with shims that emit **realistic outputs**:
     ```bash
     # mysqldump → valid gzip-able SQL prologue
     cat > "$BIN/mysqldump" <<'SH'
     #!/usr/bin/env bash
     echo "-- MariaDB dump (mock)"
     echo "CREATE DATABASE IF NOT EXISTS \`$2\`;"
     SH
     # pdnsutil list-all-zones → one fake zone
     echo -e '#!/usr/bin/env bash\necho example.test' > "$BIN/pdnsutil"
     # dpkg, nginx -t, systemctl → exit 0 / "active"
     ```
   - Calls `collect_server /tmp/staging`.
   - Assertion strategy, spec-aligned:
     * **REQUIRED** (spec items 1–9, 11 redis.conf, 12–17, 19, 20): file must
       exist under the expected path; test FAILS otherwise.
     * **OPTIONAL** (spec item 11 RDB, item 18 Bulwark files): test accepts
       either present or absent, but asserts the manifest reflects reality.
   - Runs restore against a second scratch root, confirms files landed.
   - Resilience case: delete `metadata/bulwark.json` from the snapshot, run
     restore, assert the legacy-snapshot WARN from Step 2 is emitted and the
     restorer continues (doesn't `exit`).
2. Register the test in `tests/run-tests.sh`.
3. Run under `bash -u -o pipefail` to catch unset vars.

### Verification

```bash
tests/run-tests.sh test_server_backup_roundtrip.sh
```

### Exit criteria

- [ ] Test passes locally with no real services running.
- [ ] Removing any single collector sub-function causes a clear FAIL line.
- [ ] Whole suite (`tests/run-tests.sh`) still green.

---

## Step 7 — Docs: README + ARCHITECTURE + DR runbook

**Gap:** G7. **Branch:** `docs/server-backup-completion`
**Files:** `docs/README.md`, `docs/ARCHITECTURE.md`, `docs/DISASTER-RECOVERY.md` (new)
**Depends on:** Steps 1–6.

### Context brief

`docs/README.md` lists `server-backup`, `server-restore`, `update`, `uninstall`
but is silent on `server-download`, `list server-snapshots`, the schedule
toggle, and the new flags added in Steps 1–4. A focused runbook reduces on-call
panic during an actual disaster.

### Tasks

1. `docs/README.md`: add new commands (`server-download`, `list
   server-snapshots`) and flags (`--skip-stalwart-data`, `--configs-only`).
2. `docs/ARCHITECTURE.md`: add a "Server backup layout" table mirroring the
   20-row spec, marking each row with the collector function that owns it.
3. New `docs/DISASTER-RECOVERY.md`:
   - Preconditions (fresh Ubuntu 24.04, domain DNS still valid).
   - Ordered runbook:
     1. `curl … | sudo bash` (install.sh)
     2. Restore `/etc/jabali/restic-password` from offline backup
     3. `sudo jabali-backup destination add …` (point to backup repo)
     4. `sudo jabali-backup list server-snapshots`
     5. `sudo jabali-backup server-restore --snapshot=<id>`
     6. Post-restore checks (from spec §"Post-Restore Checks")
   - What to do if DKIM broke (step 1 hasn't landed → regen + update DNS).

### Exit criteria

- [ ] All three docs exist and cross-link.
- [ ] `grep -rn "server-download\|server-restore" docs/` finds the new commands
      in usage examples.
- [ ] Runbook walks through a dry-run on a scratch VM in < 30 min.

---

---

## Review notes — incorporated fixes (round 1, 2026-04-13)

An adversarial review flagged three CRITICAL items that are now addressed
inline in the steps above; leaving breadcrumbs here so future reviewers can
audit what changed:

| Ref | Fix location |
|-----|--------------|
| Stalwart restart safety (fail collect_server if service doesn't come back) | Step 1, Task 3 |
| Bulwark legacy-snapshot warning (no bulwark.json + bulwark present) | Step 2, Task 3 |
| Dirty git working-tree capture (`git-dirty.txt` + `git-dirty.patch`) | Step 3, Task 2 |
| FPM user lookup instead of hardcoded `www-data` | Step 3, Task 5 |
| Panel-addon parity (edit `jbServerDownloadPipe` in the same PR) | Step 4, Task 4 |
| Shim outputs + REQUIRED/OPTIONAL split + restore resilience case | Step 6, Task 1 |
| DKIM "byte-identical" phrased as public-string diff, not blob diff | Step 1, Exit criteria |

Deferred, by design:
- Redis RDB snapshotting (spec says optional; add if operators ask).
- Deeper DR runbook for DKIM/DNS recovery — skeletoned in Step 7, expand when
  a real post-restore incident surfaces edge cases worth codifying.

---

## Invariants (verify after every step)

- [ ] `bash -n bin/jabali-backup lib/collectors/*.sh lib/restorers/*.sh` clean.
- [ ] `php -l panel/agent/jabali-backup.php` clean.
- [ ] `tests/run-tests.sh` green.
- [ ] No edits under `/home/shuki/projects/jabali/` (Jabali repo is read-only).
- [ ] Snapshot tags remain `type:server,date:YYYY-MM-DD,hostname:<host>` — do
      not drift to `type:full` (which would mix with per-user snapshots).
- [ ] Every panel DR action has a CLI counterpart and vice versa.

## Anti-patterns to avoid

- ❌ Duplicating the server-download build pipeline in both PHP and Bash — use
  a shared helper (Step 4 task 4).
- ❌ Restoring Stalwart data without stopping the service (RocksDB corruption).
- ❌ Running Bulwark reinstall automatically during restore without operator
  consent (internet dependency + build time).
- ❌ Logging raw restic-password or `.env` contents in any diagnostic output.
- ❌ Relabelling "Server Backup (Disaster Recovery)" to just "Server Backup" in
  UI — the "Disaster Recovery" framing is a product requirement.

## Rollback

Each step is one PR against `main`. Roll back with `git revert <merge-sha>`.
No database migrations, no external state change (snapshots from older
collectors remain restorable — new fields are additive).
