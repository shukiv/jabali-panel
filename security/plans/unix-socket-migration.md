# Plan: Unix Socket Migration

> Source PRD: Security analysis — migrate API from TCP 127.0.0.1:9876 to Unix domain socket

## Architectural decisions

Durable decisions that apply across all phases:

- **Socket path**: `/run/jabali-security/jabali-security.sock` (systemd `RuntimeDirectory` already creates `/run/jabali-security/`)
- **Socket permissions**: `0660 root:www-data` — root (daemon) and www-data (panel) can connect; hosting users cannot
- **TCP fallback**: Disabled by default. Can be re-enabled by setting `API_BIND` and `API_PORT` in config. When both are empty/unset, only socket is used.
- **Config key**: `API_SOCKET="/run/jabali-security/jabali-security.sock"` (new default)
- **Auth**: `X-API-Key` header remains for all requests (defense in depth — socket permissions are primary access control)
- **aiohttp**: Supports `UnixSite` natively — drop-in replacement for `TCPSite`

---

## Phase 1: Daemon + CLI via socket

**User stories**: Daemon listens on Unix socket. CLI commands (status, health, stop) connect via socket. Socket has proper file permissions.

### What to build

The daemon creates a `UnixSite` listener on `/run/jabali-security/jabali-security.sock` instead of (or alongside) `TCPSite`. After the socket is created, its permissions are set to `0660 root:www-data`. The CLI `_api_request()` function detects the socket path from config and connects via `urllib` with a Unix socket adapter (or `http.client` with socket override). A new config key `API_SOCKET` is added with the default path. If `API_BIND` is also set, both listeners start (dual-stack for migration).

### Acceptance criteria

- [ ] Daemon starts and creates `/run/jabali-security/jabali-security.sock`
- [ ] Socket has permissions `0660 root:www-data`
- [ ] `curl --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/health` returns OK
- [ ] `jabali-security status` works via socket
- [ ] Non-root, non-www-data users get "Permission denied" on the socket
- [ ] If `API_BIND` is set in config, TCP listener also starts

---

## Phase 2: Panel plugin via socket

**User stories**: The Jabali Panel Filament plugin connects via Unix socket instead of TCP.

### What to build

`panel/JabaliSecurityClient.php` uses Laravel's HTTP client with `CURLOPT_UNIX_SOCKET_PATH` option. The base URL changes from `http://127.0.0.1:9876/api/v1` to `http://localhost/api/v1` with the socket option. Socket path is read from `/etc/jabali-security/jabali-security.conf` (same as API key). Falls back to TCP URL if socket doesn't exist.

### Acceptance criteria

- [ ] Panel Security page loads all tabs via socket
- [ ] Module toggles work
- [ ] Config editing works
- [ ] Table actions (resolve, block, add rule) work
- [ ] Panel works without TCP listener running

---

## Phase 4: Tests + TCP deprecation

**User stories**: Test suite validates socket connectivity. TCP is disabled by default. Installer and updater handle migration.

### What to build

`tests/test_security.sh` phase 4b tests connect via socket instead of TCP port. The installer sets `API_SOCKET` in config and removes/comments `API_BIND`/`API_PORT` defaults. The `jabali-security update` command migrates existing configs. Documentation updated to reflect socket-first architecture. External API test (Phase 6) confirms port 9876 is no longer listening.

### Acceptance criteria

- [ ] `test_security.sh` passes with socket-based API tests
- [ ] Fresh install: only socket listener, no TCP
- [ ] `API_BIND`/`API_PORT` in config re-enables TCP as fallback
- [ ] `jabali-security update` migrates existing configs
- [ ] Port 9876 not open after fresh install (`ss -tlnp` shows no jabali-security TCP)
- [ ] Documentation updated (API.md, ARCHITECTURE.md, CONFIGURATION.md)
