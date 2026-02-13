# Installation

## Debian packages (no install.sh)

Jabali ships as two Debian packages:

- `jabali-deps` — system dependencies (nginx, PHP, DB, mail, DNS, etc.)
- `jabali-panel` — the panel application + systemd services

### Build the packages

From the repository root:

```
./scripts/build-jabali-deps-deb.sh
./scripts/build-jabali-panel-deb.sh
```

This produces:

```
jabali-deps_<version>_all.deb
jabali-panel_<version>_all.deb
```

### Install on a server

```
sudo dpkg -i ./jabali-deps_<version>_all.deb
sudo apt-get -f install -y
sudo dpkg -i ./jabali-panel_<version>_all.deb
```

After install, systemd services are enabled and started:

- `jabali-agent`
- `jabali-queue`
- `jabali-health-monitor`

## Demo Docker (single container)

Demo images run the panel in read-only mode with demo data preloaded.

Key requirements:

- `JABALI_DEMO=1`
- `DB_DATABASE` must point to the demo SQLite file: `database/database-demo.sqlite`
- Trust reverse proxy so Livewire update URLs are HTTPS

Example container run (port 5555):

```
docker run -d --name jabali-panel-demo \
  --restart unless-stopped \
  -p 5555:5555 \
  -e APP_URL=https://demo.jabali-panel.com \
  -e ASSET_URL=https://demo.jabali-panel.com \
  -e DB_DATABASE=/var/www/jabali/database/database-demo.sqlite \
  -e JABALI_DEMO=1 \
  -e JABALI_DEMO_ADMIN_EMAIL=admin@jabali-panel.com \
  -e JABALI_DEMO_ADMIN_PASSWORD=demo1234 \
  -e JABALI_DEMO_USER_EMAIL=demo@jabali-panel.com \
  -e JABALI_DEMO_USER_PASSWORD=demo1234 \
  jabali-panel-demo-current
```

Nginx reverse proxy (HTTPS):

```
server {
    listen 443 ssl http2;
    server_name demo.jabali-panel.com;
    ssl_certificate /etc/letsencrypt/live/demo.jabali-panel.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/demo.jabali-panel.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:5555;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection upgrade;
    }
}
```

Reverse proxy trust (required for Livewire HTTPS):

- Ensure `bootstrap/app.php` includes `trustProxies(at: '*')`.

Demo limitations:

- No agent socket in the container: pages that rely on the agent should use
  static demo data to avoid 500s.

## Panel notifications (admin + user)

Jabali ships with a hardened Filament notifications setup that prevents Livewire
success hooks from breaking after the first toast.

What is included:

- `public/js/filament/notifications/notifications.js` is patched to guard the
  animation callback (prevents `TypeError: e is not a function`).
- `resources/views/vendor/filament-notifications/notifications.blade.php` adds
  a lightweight `wire:poll.2s` so toasts keep flowing even if a Livewire event
  is dropped.

If you update or rebuild assets, keep the guard in place and hard‑refresh the
browser (Ctrl+Shift+R) after deployment.

## Deploy script

The repository ships with a deploy helper at `scripts/deploy.sh`. It rsyncs the
project to `root@192.168.100.50:/var/www/jabali`, commits on that server, bumps
`VERSION`, updates the `install.sh` fallback, and pushes to Git remotes from
that server. Then it runs composer/npm, migrations, and cache rebuilds.

Defaults (override via flags, env vars, or config.toml):

Config file:
- `config.toml` (ignored by git) is read automatically if present.
- Start from `config.toml.example`.
- Set `CONFIG_FILE` to use an alternate TOML file path.
- Supported keys are in `[deploy]` (for example: `host`, `user`, `path`, `www_user`, `push_branch`, `gitea_url`, `github_url`).

- Host: `192.168.100.50`
- User: `root`
- Path: `/var/www/jabali`
- Web user: `www-data`

Common usage:
```
# Basic deploy to the default host
scripts/deploy.sh

# Target a different host/path/user
scripts/deploy.sh --host 192.168.100.50 --user root --path /var/www/jabali --www-user www-data

# Dry-run rsync only
scripts/deploy.sh --dry-run

# Skip npm build and cache steps
scripts/deploy.sh --skip-npm --skip-cache
```

Push behavior controls:
```
# Deploy only (no push)
scripts/deploy.sh --skip-push

# Push only one remote
scripts/deploy.sh --no-push-github
scripts/deploy.sh --no-push-gitea

# Push to explicit URLs from the test server
scripts/deploy.sh --gitea-url ssh://git@192.168.100.100:2222/shukivaknin/jabali-panel.git \
  --github-url git@github.com:shukiv/jabali-panel.git
```

GitHub push location:
```
# Push to GitHub from the test server (required)
ssh root@192.168.100.50
cd /var/www/jabali
git push origin main
```

Notes:
- Pushes run from `root@192.168.100.50` (not from local machine).
- Before each push, the script bumps `VERSION`, updates `install.sh` fallback,
  and commits on the deploy server.
- Rsync excludes `.env`, `storage/`, `vendor/`, `node_modules/`, `public/build/`,
  `bootstrap/cache/`, and SQLite DB files. Handle those separately if needed.
- `--delete` passes `--delete` to rsync (dangerous).

## Testing after changes

After every change, run a test to make sure there are no errors.
