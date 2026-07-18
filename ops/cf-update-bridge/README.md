# `jabali update` → GitHub release bridge (Cloudflare Worker)

Transitional bridge so **already-deployed hosts** keep updating after the forge
moved to **GitHub** (`shukiv/jabali-panel`). The forge history is retired Gitea
(`git.jabali-panel.com`) → Codeberg → GitHub; GitHub is the sole source of truth
after the 2026-07 cutover.

## The problem it solves

Every older deployed host has `https://git.jabali-panel.com/api/v1/repos/shukivaknin/jabali2`
compiled into its binary (git `origin` + release API base). A host only gets a
new binary *by updating* — from that old URL. Chicken-and-egg.

`worker.js` sits on `git.jabali-panel.com` and **301-redirects** the jabali2
release-API and git-smart-HTTP paths to their GitHub equivalents, so existing
hosts transparently pull from GitHub at their old URL. The **first** such update
hands them a GitHub-pointing binary (the updater also self-heals `origin` to
GitHub on every check), after which they bypass the bridge. Self-liquidating.

Path mapping (see `RULES` in `worker.js`):

| old path on git.jabali-panel.com | → GitHub |
|---|---|
| `/api/v1/repos/shukivaknin/jabali2/…` | `https://api.github.com/repos/shukiv/jabali-panel/…` |
| `/shukivaknin/jabali2.git/…` | `https://github.com/shukiv/jabali-panel.git/…` |
| `/shukivaknin/jabali2/info/refs` | `https://github.com/shukiv/jabali-panel/info/refs` |

## Deploy (routes on the git.jabali-panel.com zone)

The Worker covers three prefixes, so register all three routes (or a broader
pattern that only matches jabali2 paths — do NOT catch-all the zone; the old
Gitea still hosts other repos there).

### Option A — wrangler (recommended)
```bash
# wrangler.toml:
#   name = "jabali-update-bridge"
#   main = "worker.js"
#   compatibility_date = "2024-11-01"
#   routes = [
#     { pattern = "git.jabali-panel.com/api/v1/repos/shukivaknin/jabali2/*", zone_name = "jabali-panel.com" },
#     { pattern = "git.jabali-panel.com/shukivaknin/jabali2.git/*",          zone_name = "jabali-panel.com" },
#     { pattern = "git.jabali-panel.com/shukivaknin/jabali2/info/refs*",     zone_name = "jabali-panel.com" },
#   ]
wrangler deploy
```

### Option B — Cloudflare API
1. Upload the script:
   `PUT /accounts/{account_id}/workers/scripts/jabali-update-bridge` (body = worker.js, content-type application/javascript+module).
2. Add each route on the zone:
   `POST /zones/{zone_id}/workers/routes` with each `pattern` above and
   `"script": "jabali-update-bridge"`.

## Verify BEFORE relying on it

```bash
# 301 to the api.github.com equivalent (follow it → same release JSON as GitHub)
curl -sI https://git.jabali-panel.com/api/v1/repos/shukivaknin/jabali2/releases?per_page=1 | grep -i '^location'
curl -fsSL https://git.jabali-panel.com/api/v1/repos/shukivaknin/jabali2/releases?per_page=1 | head
# The browser_download_url in the followed response MUST be a github.com URL.
```

Then do a real `jabali update` (or its dry-run) on a canary host and confirm it
resolves + installs + passes sha256.

## Scope / caveats

- Scoped to jabali2 release-API + git paths — the old Gitea web UI/issues on
  this domain are NOT redirected.
- **Retire when the fleet has rolled over** — once hosts report a
  GitHub-pointing version (and `origin` self-healed to GitHub), remove the
  Worker + the DNS record.
- The GitHub repo is public, so the bridge needs no token. Unauthenticated
  GitHub API calls are rate-limited (60/h/IP); a single update is one call.
