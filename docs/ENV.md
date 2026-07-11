# Environment Variables

Reference for every env var the panel reads at runtime. Canonical source:
`panel-api/internal/config/config.go`. Non-secret config template lives in
[`config.example.toml`](../config.example.toml); secrets are set via the
environment (systemd `EnvironmentFile=` in prod, `direnv`/manual export
in dev).

**Precedence:** env var > `config.toml` > hardcoded default. Put
non-secrets in TOML, secrets in the environment.

## Server + runtime

<!-- AUTO-GENERATED:env-server — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PANEL_ADDR` | No | `127.0.0.1:8443` | HTTP listen address. See [ADR-0014](adr/0014-panel-port-8443-user-443.md). |
| `PANEL_ENV` | No | `development` | `development` or `production`. In `production`, log format auto-upgrades to JSON unless `LOG_FORMAT=text`. |
| `TLS_CERT` | No | *(unset)* | Path to TLS cert. If both `TLS_CERT` and `TLS_KEY` are set, the panel serves HTTPS directly. Usually unset: nginx fronts the panel and TLS-terminates for it. |
| `TLS_KEY` | No | *(unset)* | Path to TLS key. Pairs with `TLS_CERT`. |
| `JABALI_CONFIG` | No | `/etc/jabali/config.toml` | Path to the TOML config file. |
<!-- /AUTO-GENERATED -->

## Logging

<!-- AUTO-GENERATED:env-log — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LOG_LEVEL` | No | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `LOG_FORMAT` | No | `text` (dev) / `json` (prod) | `text` \| `json`. Explicitly set to override the env-driven default. |
<!-- /AUTO-GENERATED -->

## Database

<!-- AUTO-GENERATED:env-db — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes (Phase 3+) | *(unset)* | MariaDB DSN, e.g. `mysql://jabali_panel:PASS@tcp(127.0.0.1:3306)/jabali_panel?parseTime=true&charset=utf8mb4&loc=UTC`. |
| `SKIP_MIGRATIONS` | No | `false` | If truthy, skip migrate-on-start. Useful for out-of-band migrations. |
| `JABALI_TEST_DATABASE_URL` | No | *(unset)* | Integration test DSN. When set, `make test-integration` / `make coverage-check` run against it. Must point at an empty test DB the test runner can drop/recreate. |
<!-- /AUTO-GENERATED -->

## Auth (Kratos)

Since M20 (ADR-0034), Kratos owns sessions; the panel reads them via the
whoami endpoint. JWT_* / AUTH_COOKIE_SECURE were removed when the legacy
stack was deleted — do NOT re-introduce them.

<!-- AUTO-GENERATED:env-auth — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KRATOS_PUBLIC_URL` | No | `unix:/run/jabali-kratos/public.sock` | Base URL of the Kratos public API. The panel-api whoami client dials this to validate session cookies. M25 ships the unix-socket form; TCP `http://localhost:4433` is the dev fallback. |
| `KRATOS_ADMIN_URL` | No | `unix:/run/jabali-kratos/admin.sock` | Base URL of the Kratos admin API. Used by impersonation + admin identity ops. M25 ships the unix-socket form; TCP `http://localhost:4434` is the dev fallback. |
| `JABALI_BOOTSTRAP_ADMIN_EMAIL` | No | *(unset)* | If set at first boot, creates an admin with this email. Paired with `JABALI_BOOTSTRAP_ADMIN_PASSWORD`. |
| `JABALI_BOOTSTRAP_ADMIN_PASSWORD` | No | *(unset)* | Plaintext bootstrap password. Used once; the row is seeded and the var can be removed. |
<!-- /AUTO-GENERATED -->

## Agent (panel-api → agent client)

<!-- AUTO-GENERATED:env-agent — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `AGENT_SOCKET` | No | `/run/jabali/agent.sock` | Path to the panel-agent Unix socket. Panel user must have rw on the socket. See [ADR-0001](adr/0001-go-agent-over-ndjson-unix-socket.md). |
| `AGENT_TIMEOUT` | No | `30s` | Default per-call wall-clock ceiling. Short commands (e.g. `agent.version`) set their own tighter deadline. |
<!-- /AUTO-GENERATED -->

## Agent (jabali-agent binary)

These are read by the agent process itself. Prod installs set them via the
systemd unit `install.sh` writes.

<!-- AUTO-GENERATED:env-agent-binary — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `JABALI_AGENT_SOCKET` | No | `/run/jabali/agent.sock` | Unix socket the agent listens on. Must match `AGENT_SOCKET` on the panel side. |
| `JABALI_AGENT_GID` | No | `-1` (skip) | If ≥ 0, the agent `chown`s the socket to `root:<gid>` after bind so only that group (typically `jabali`) can connect. |
| `JABALI_AGENT_LOG_FORMAT` | No | `json` | `json` \| `text`. Prod is JSON so journald parsing is trivial. |
| `JABALI_AGENT_LOG_LEVEL` | No | `info` | `debug` \| `info` \| `warn` \| `error`. |
<!-- /AUTO-GENERATED -->

## CORS + SSL

<!-- AUTO-GENERATED:env-cors-ssl — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `CORS_ALLOWED_ORIGINS` | No | *(empty = no CORS)* | Comma-separated list of origins allowed to hit the API from a browser. Add the SPA's origin in dev. |
| `JABALI_ACME_STAGING_ONLY` | No | `false` | Force Let's Encrypt staging directory for all ACME requests. Use for development + ACME rate-limit recovery. See [ADR-0017](adr/0017-ssl-try-acme-then-selfsigned-with-backoff.md). |
<!-- /AUTO-GENERATED -->

## SSO (phpMyAdmin single-sign-on)

M7 Tranche E foundation. Parked pending M9 (see
[ADR-0022](adr/0022-m7-phpmyadmin-sso-shadow-account-and-uds.md)) — the
key loader is wired, unused until SSO work resumes.

<!-- AUTO-GENERATED:env-sso — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `JABALI_SSO_KEY_PATH` | No | `/etc/jabali-panel/sso.key` | Path to the 32-byte AES-256-GCM key used to encrypt shadow MariaDB admin passwords at rest. Must be mode `0600`, owner `jabali:jabali`. `install.sh install_sso_key` writes this on first install. If missing, the SSO feature is disabled (handler returns 503); startup continues. |
| `JABALI_SSO_SOCKET_PATH` | No | `/run/jabali-panel/sso.sock` | Unix socket used by the phpMyAdmin SSO validation round-trip. Read by the SSO service; paired with the sign-on handoff PHP. |
<!-- /AUTO-GENERATED -->

## WordPress reconciler

These tune the WordPress install/clone/delete timeout windows and the per-tick probe batch. Zero values disable the corresponding stale-row sweep.

<!-- AUTO-GENERATED:env-wordpress — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WORDPRESS_INSTALL_TIMEOUT` | No | `10m` | Max age for a WordPress install stuck in `installing` state before the reconciler marks it failed. Accepts any `time.ParseDuration` string (`30s`, `5m`, `1h`). |
| `WORDPRESS_CLONE_TIMEOUT` | No | `30m` | Max age for a WordPress install stuck in `cloning` state. Larger than install because cloning copies a full docroot + DB. |
| `WORDPRESS_DELETE_TIMEOUT` | No | `5m` | Max age for a WordPress install stuck in `deleting` state. Short: delete is fast; anything longer is a stuck agent call. |
| `WORDPRESS_PROBE_BATCH` | No | `100` | Max number of ready WordPress installs the reconciler liveness-probes per tick. Lower to cap CPU/DB load on large hosts. |
<!-- /AUTO-GENERATED -->

## Path overrides (agent)

Operator-visible overrides for paths the agent writes to. Defaults match
what `install.sh` creates; only set these if you're relocating state
(e.g. multi-tenant hosts with per-instance roots).

<!-- AUTO-GENERATED:env-path-overrides — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `JABALI_FPM_CONFIG_ROOT` | No | `/etc/jabali-panel/fpm` | Directory holding per-user FPM global configs (`<user>.conf`). Read by `php.pool.apply` and `php.pool.remove`. |
| `JABALI_PHP_VER_PIN_ROOT` | No | `/etc/jabali-panel/user-phpver` | Directory holding version-pin side files (one per user, contents `8.5\n`). Read by the FPM pre-start shim and pool-apply. |
| `JABALI_PDNS_ENV_FILE` | No | `/etc/jabali-panel/pdns.env` | PowerDNS MySQL connection file (`PDNS_DB_HOST=…`, etc.). Missing file = DNS feature disabled cleanly (agent boots). |
<!-- /AUTO-GENERATED -->

## Test-only overrides

These are used by `*_test.go` files for isolation. Not intended for
production use; listed here so the one-liner env audit stays honest.

<!-- AUTO-GENERATED:env-test-overrides — regenerate via /update-docs -->
| Variable | Purpose |
|----------|---------|
| `JABALI_SYSTEMD_ROOT` | Override `/etc/systemd/system` in `user.slice.ensure` tests. |
| `JABALI_PHP_POOL_CONFIG_DIR` | Override `/etc/php/<v>/fpm/pool.d` in `php.pool.apply` tests. |
| `JABALI_PHP_POOL_TEMPLATE_PATH` | Override `/etc/jabali-panel/php-pool.conf.tmpl` in `php.pool.apply` tests. |
| `JABALI_PHP_POOL_SKIP_RELOAD` | Set to non-empty to skip `systemctl reload` in pool-apply/remove tests. |
| `JABALI_SSHD_DROPIN_PATH` | Override `/etc/ssh/sshd_config.d/jabali-sshd.conf` in `system.set_ssh_config` tests. |
| `JABALI_SSHD_SFTP_DROPIN_PATH` | Override `/etc/ssh/sshd_config.d/jabali-sftp.conf` (M12 Match Group drop-in) in tests. |
| `JABALI_SSHD_TEST_SKIP_VALIDATE` | Set to non-empty to skip the `sshd -t` config validation exec in tests. |
| `JABALI_SSHD_TEST_SKIP_RELOAD` | Set to non-empty to skip the `systemctl reload sshd` exec in tests. |
<!-- /AUTO-GENERATED -->

## Install-time (read by `install.sh` / CLI subcommands)

<!-- AUTO-GENERATED:env-install — regenerate via /update-docs -->
| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `JABALI_GO_VERSION` | No | `1.26.4` | Go version `install.sh` downloads on a clean box. |
| `JABALI_GO_ROOT` | No | `/usr/local/go` | Where the installer writes the Go toolchain. |
| `JABALI_SERVICE_USER` | No | `jabali` | Service account the panel + agent run as. |
| `JABALI_REPO_DIR` | No | `/opt/jabali2` | Git checkout path on the target host. |
| `JABALI_REPO_URL` | No | `https://github.com/shukiv/jabali-panel.git` | Override the git remote `install.sh` clones from (useful for private mirrors). |
| `JABALI_REPO_BRANCH` | No | `main` | Branch `install.sh` checks out. |
| `JABALI_GITEA_TOKEN` | No | *(unset)* | Personal access token for private Gitea mirror. Used by `install.sh` when the source repo requires auth. Equivalent to the first positional arg to `install.sh`. |
| `JABALI_PHP_VERSIONS` | No | `8.5` | Space-separated list of PHP versions `install.sh install_php` fetches from the Sury repo. Supported range: 7.4 through 8.5. Example: `JABALI_PHP_VERSIONS="7.4 8.2 8.5" bash install.sh`. See [ADR-0023](adr/0023-m9-php-fpm-pool-manager.md). |
| `JABALI_PANEL_ADDR` | No | `0.0.0.0:8443` | Panel listen address written into `config.toml` on first install. Seeds `server_settings.addr`; DB is the source of truth after first boot. |
| `JABALI_HOSTNAME` | No | *(unset)* | If set, `install.sh` runs `hostnamectl set-hostname` before configuring nginx and certs. Leave unset to preserve the host's current hostname. |
| `JABALI_DEV` | No | `0` | If `1`, `install.sh` sets `PANEL_ENV=development` (log format stays text, TLS verification relaxed in relevant paths). |
| `JABALI_TMP_SIZE` | No | `1G` | Size cap for the M18 tmpfs `/tmp` mount (`configure_tmp_tmpfs`). Accepts any tmpfs-compatible size string (e.g. `2G`, `512M`). See [ADR-0032](adr/0032-m18-resource-limits.md). |
<!-- /AUTO-GENERATED -->

## Adding a new env var

1. Read it in `panel-api/internal/config/config.go` (and fail loud at
   startup if required secrets are missing).
2. Add a commented row to `config.example.toml` if it has a TOML
   counterpart.
3. Re-run `/update-docs` (or hand-edit this file) so the table stays
   in sync.
