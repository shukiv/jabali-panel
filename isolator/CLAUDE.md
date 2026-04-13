# jabali-isolator

Lightweight nspawn container isolation for SSH shell access. Each hosting user gets a container with mount/PID/IPC isolation and cgroup resource limits. Web serving uses host PHP-FPM pools directly — no FPM inside containers.

## Architecture

```
config.py        Constants, validation (USERNAME_RE, validate_memory/cpu)
machine.py       Single source of truth for {user}-php naming + all derived paths
rootfs.py        Minimal rootfs builder (passwd, group, resolv.conf, os-release, mount-point dirs)
units.py         .nspawn unit + service drop-in generators, validates inputs before interpolation
container.py     Async orchestrator — create/start/stop/destroy/status/list with per-user fcntl locking
__main__.py      Click CLI entry point (jabali-isolate)
```

Dependency flow: `config` <- `machine` <- `rootfs`/`units` <- `container` <- `__main__`

## Container Design

- **Main process**: `sleep infinity` (keeps container alive for nsenter)
- **No auto-start on boot**: containers start on-demand via SSH login (`jabali-shell`)
- **Idle cleanup**: systemd timer (`jabali-container-idle-check`) stops containers with no active SSH sessions every 5 min
- **Default memory limit**: 256M per container
- **Rootfs includes**: `/etc/os-release` (required for VS Code Remote SSH platform detection)
- **Bind mounts**: `/usr`, `/lib`, `/lib64`, `/bin`, `/sbin`, `/etc/php`, `/etc/ssl/certs`, `/home/{user}` (all RO except home)

## Key Design Decisions

- All subprocess calls use list args via `asyncio.create_subprocess_exec` (no shell)
- Username validated with `^[a-z_][a-z0-9_.-]{0,31}$` at every public entry point
- Per-user advisory file lock (`fcntl.flock` on `/run/jabali-isolator/{user}.lock`) prevents concurrent create/destroy/start/stop races
- `_stop_unlocked()` internal helper avoids lock re-acquisition in destroy/restart
- `status()` and `list_all()` are read-only and do NOT lock
- `list_all()` uses `asyncio.gather()` for parallel status checks
- `machine.py` uses late-bound config so monkeypatch works from a single patch site
- Resource limits validated before writing to systemd unit files
- `shutil.rmtree()` paths are canonicalized and verified to stay under expected parent dirs
- `PrivateUsers=no` in nspawn unit — user namespaces disabled so nsenter/setpriv can map UIDs correctly

## Build & Test

```bash
uv sync                    # install deps
uv run pytest -v           # run tests
uv run ruff check .        # lint
uv run ruff format .       # format
```

## Testing Conventions

- Shared fixtures in `tests/conftest.py`: `isolator_dirs` (patches all config paths to tmp_path), `fake_user`
- Async tests use `@pytest.mark.asyncio` with `asyncio_mode = "auto"`
- Subprocess calls mocked via `patch("jabali_isolator.container._run", new_callable=AsyncMock)`
- User lookup mocked via `patch("jabali_isolator.rootfs._lookup_user")`

## Git

- Gitea: `gitea:shukivaknin/jabali-isolator.git`
- GitHub: `git@github.com:shukiv/jabali-isolator.git`
- Branch: `master`
