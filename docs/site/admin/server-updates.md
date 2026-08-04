# Server Updates

`/jabali-admin/updates`. M29. Run `jabali update` from the panel UI as a transient systemd unit (ADR-0064).

## What an update does

1. `git fetch origin main`
2. `git reset --hard origin/main`
3. Rebuild `jabali-panel-api` and `jabali-agent` (Go).
4. `npm ci && npm run build` for `panel-ui`.
5. Apply pending database migrations.
6. Re-apply install-time host configurations the agent owns (nginx drop-ins, php-fpm pool directories, AppSec rules, FastCGI cache keyzone). The principle is "`install.sh` is truth, not runbook" — anything needed for a fresh host to work is also re-applied on every update.
7. Reload nginx, restart the panel API, restart the agent, restart Bulwark, in that order.
8. Report success or the first failure line.

## Why transient systemd units

If the update were to run inside the panel API itself, the panel restart in step 7 would kill the update process mid-step. The page would then look hung; the operator would have no idea whether the update succeeded.

Running each update as a `systemd-run --transient --unit=jabali-update-<timestamp>` job sidesteps this. The page subscribes to the unit's journal, so:

- The panel can restart itself mid-update without interrupting the update process.
- The operator can close and reopen the page; the live log reattaches.
- A failed update leaves a named unit (`jabali-update-2026-05-27-…`) that `journalctl -u <unit>` retains for the operator to inspect afterwards.

The transient-unit pattern was live-verified on 192.168.100.150.

## Update window

Server Settings → Updates → **Update window**. If set, `jabali update --auto` refuses to run outside the window. UI-initiated updates ignore the window (operator-driven, presumed deliberate).

## Why a fixed bug came back

The most confusing failure this system produces is a bug that was fixed, merged,
and then re-appeared in production. It is almost always deploy drift, and the
mechanism is worth understanding because the symptom points away from the cause.

**A merged fix is inert until the binary on that box updates.** That much is
obvious. What is not obvious is that an out-of-date binary does not merely *lack*
the fix — it can actively **undo** one.

Several host configuration files are rendered from templates **embedded in the
Go binary**, not read from disk: the CRS before-plugin, the AppSec config, nginx
drop-ins, PHP-FPM pool files. Step 6 of every update, and the reconciler on its
own schedule, re-render those files from whatever template the running binary
carries. So on a box whose binary predates a fix:

- a hand-applied correction is **overwritten** by the old embedded template on the
  next reconcile, and
- it comes back silently, because re-rendering a managed file is normal behaviour,
  not an error.

Two production incidents on one fleet in three weeks came from exactly this:

- An AppArmor fix existed but was not deployed; config convergence restored the
  shipped (unfixed) template overnight and took a customer's site down a **second**
  time.
- CRS false-positive exclusions for WooCommerce checkout merged on one day, and
  real customers were still getting 4-hour CrowdSec bans mid-checkout the **next**
  day, because `appsec render-config` on the old binary rewrote the exclusion file
  from the old embedded template.

### Checking for drift

```
jabali version                 # the commit this box is actually running
```

The Updates page (`/jabali-admin/updates`) shows how many commits the box is
behind its release channel; the same number is available from the admin API at
`GET /api/v1/admin/updates/state` as `behind_count`. There is no CLI flag for it
today — `jabali version` plus the panel is the check.

Any non-zero behind-count means at least one merged fix is not in force here, and
that config convergence may be actively re-applying pre-fix templates.

### Avoiding it

Enable auto-update on the **stable** channel. Stable is by definition the
reviewed, promoted build, which is what unattended convergence wants: the box
converges on fixes without an operator remembering to deploy each one.

Auto-update ships **off**, so this is opt-in — configure it on the Updates page
(`/jabali-admin/updates`), which drives the `jabali-autoupdate.timer` through the
reconciler. A fleet that is not
auto-updating needs a deliberate deploy step after every merge that matters, and
"we merged the fix" is not the same statement as "the fix is in force".

## Common failure modes

| Symptom | Cause | Resolution |
|---|---|---|
| `git reset --hard` fails | Local edits to a tracked file. | Investigate via `git status` before discarding; once safe, retry. |
| `npm ci` ENOTEMPTY | Race condition cleaning `node_modules`. | Retry; second run succeeds. |
| `npm run build` exit 137 | Vite OOM on a small VM. | The installer caps `NODE_OPTIONS=--max-old-space-size` and auto-creates swap on first OOM. If it persists, increase VM RAM. |
| Migration `Dirty database version N` | A previous migration failed mid-flight. | `jabali migrate up` to retry. |
| Service fails to start in step 7 | Unit file shipped by the new commit references a path that does not exist yet. | `jabali repair --diagnose` will identify; usually a follow-up commit fixes within hours. |

## Post-update hint

On any failure, the panel prints a hint pointing at `jabali repair --diagnose`. The hint was added after recurring deploy scars where the operator needed `repair` next anyway.

## CLI

```bash
jabali update          # interactive
jabali update --auto   # unattended, respects update window
```
