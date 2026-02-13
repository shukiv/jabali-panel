# AGENTS.md

Rules and behavior for automated agents working on Jabali.

## Baseline
- Read `AGENT.md` first; it is the authoritative project guide.
- Work from `/var/www/jabali`.
- Use ASCII in edits unless the file already uses Unicode.

## Editing
- Prefer `rg` for search; fall back to `grep` if needed.
- Use `apply_patch` for small manual edits.
- Avoid `apply_patch` for generated files (build artifacts, lockfiles, etc.).
- Avoid destructive git commands unless explicitly requested.

## Git
- Do not push unless the user explicitly asks.
- Bump `VERSION` before every push.
- Keep `install.sh` version fallback in sync with `VERSION`.
- Push to GitHub from `root@192.168.100.50`.

## Operational
- If you add dependencies, update both install and uninstall paths.
- For installer/upgrade changes, consider ownership/permissions for:
  - `storage/`, `bootstrap/cache/`, `public/build/`, `node_modules/`.
- Run tests if available; if not, report what is missing.
