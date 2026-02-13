# DECISIONS.md

## 2026-02-01
- Server status charts read from sysstat logs via `SysstatMetrics` (no internal server_metrics table).
- Upgrade command manages npm caches in `storage/` and skips puppeteer downloads.
- Asset builds must be writable for `public/build` and `node_modules`; upgrade checks both.
- Installer builds assets as `www-data` to avoid permission issues.
- Default panel database is SQLite (`database/database.sqlite`).

## 2026-02-03
- Demo mode is enforced by `App\Http\Middleware\DemoReadOnly` (read-only, but allow Livewire `authenticate`).
- Demo container runs without agent sockets; select pages use static demo data to avoid 500s.
- Reverse proxies must be trusted for HTTPS Livewire updates in demo (`trustProxies(at: '*')`).
