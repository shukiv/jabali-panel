# Archive Notes (Cleanup 2026-01-24)

Archived to `/opt/jabali-archive/cleanup-20260124/repo-root`:

- `info.php` (unused debug phpinfo page; no repo references)
- `screenshot.mjs` (unused local script; no repo references)
- `screenshot-user-dashboard.js` (unused local script; no repo references)
- `test-cpanel-migration.mjs` (unused local script; no repo references)
- `test-flow.mjs` (unused local script; no repo references)
- `jabali_logo.svg` (duplicate of `public/images/jabali_logo.svg`)

Restore by moving the files back to the repo root if needed.

## Demo housekeeping (2026-02-03)

Disk cleanup steps used on demo hosts:

- Vacuum systemd journals (`journalctl --vacuum-time=7d`)
- Clean apt cache (`apt-get clean` + remove `/var/lib/apt/lists/*`)
- Prune Docker build cache (`docker builder prune -af`)
