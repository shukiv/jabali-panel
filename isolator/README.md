# jabali-isolator

PHP-FPM container isolation via systemd-nspawn.

Replaces PHP's `open_basedir` with kernel-level namespace isolation. Each hosting user gets a lightweight container with:

- **Mount isolation** — user only sees their own home directory + read-only system binaries
- **PID isolation** — can't see or signal other users' processes
- **Resource limits** — per-user memory and CPU caps via cgroups
- **No disk overhead** — host binaries are bind-mounted read-only, no duplication

---

## Table of Contents

- [Requirements](#requirements)
- [Install](#install)
- [CLI Reference](#cli-reference)
  - [create](#create)
  - [start](#start)
  - [stop](#stop)
  - [restart](#restart)
  - [status](#status)
  - [list](#list)
  - [destroy](#destroy)
- [Features](#features)
  - [Container Lifecycle Management](#container-lifecycle-management)
  - [Minimal Rootfs Builder](#minimal-rootfs-builder)
  - [systemd-nspawn Integration](#systemd-nspawn-integration)
  - [Resource Limits (cgroups)](#resource-limits-cgroups)
  - [Mount Namespace Isolation](#mount-namespace-isolation)
  - [PID Namespace Isolation](#pid-namespace-isolation)
  - [PHP-FPM Socket Bridge](#php-fpm-socket-bridge)
  - [Username Validation and Security](#username-validation-and-security)
  - [JSON Output Mode](#json-output-mode)
  - [Async Subprocess Execution](#async-subprocess-execution)
  - [Idempotent Operations](#idempotent-operations)
- [Architecture](#architecture)
  - [Module Overview](#module-overview)
  - [Container Structure](#container-structure)
  - [systemd Unit Files](#systemd-unit-files)
  - [Execution Flow](#execution-flow)
- [Isolation Details](#isolation-details)
  - [What's Isolated](#whats-isolated)
  - [What's NOT Isolated](#whats-not-isolated)
- [Panel Integration](#panel-integration)
- [Comparison with open_basedir](#comparison-with-open_basedir)
- [Development](#development)
  - [Running Tests](#running-tests)
  - [Project Structure](#project-structure)
- [License](#license)

---

## Requirements

- Linux with systemd 245+
- `systemd-container` package (`apt install systemd-container`)
- Python 3.12+
- Root access (required for namespace operations and bind mounts)

## Install

```bash
pip install jabali-isolator
```

Or from source:

```bash
cd jabali-isolator
uv sync
```

---

## CLI Reference

All commands (except `status` and `list`) require root privileges. The CLI entry point is `jabali-isolate`.

### create

Create a container for a user. Builds the rootfs, writes systemd unit files, and creates the host-side socket directory. Does **not** start the container.

```bash
sudo jabali-isolate create <user> [--memory LIMIT] [--cpu QUOTA]
```

| Option | Default | Description |
|---|---|---|
| `--memory` | `512M` | Memory limit per container. Accepts systemd memory notation (e.g., `256M`, `1G`, `2G`). |
| `--cpu` | `100%` | CPU quota. `100%` = 1 full core, `200%` = 2 cores, `50%` = half a core. |

**What it does:**

1. Validates the username against the allowed pattern
2. Checks that `systemd-nspawn` is installed on the host
3. Looks up the user in the host's `/etc/passwd` (user must already exist)
4. Creates a minimal rootfs at `/var/lib/machines/{user}-php/`
5. Writes `/etc/passwd` and `/etc/group` inside the rootfs with only `root` + the target user
6. Copies the host's `/etc/resolv.conf` into the rootfs for DNS resolution
7. Creates empty mount-point directories (`/usr`, `/lib`, `/bin`, etc.)
8. Writes the `.nspawn` unit file at `/etc/systemd/nspawn/{user}-php.nspawn`
9. Writes the service drop-in at `/etc/systemd/system/systemd-nspawn@{user}-php.service.d/limits.conf`
10. Creates the host-side socket directory at `/run/jabali-fpm/{user}/`
11. Runs `systemctl daemon-reload` so systemd picks up the new units
12. Runs `systemctl enable systemd-nspawn@{user}-php.service` for auto-start on boot

**Example:**

```bash
sudo jabali-isolate create user1 --memory 1G --cpu 200%
# Created container for user1
#   Rootfs:  /var/lib/machines/user1-php
#   Memory:  1G
#   CPU:     200%
#
# Start with: jabali-isolate start user1
```

---

### start

Start an existing container via `machinectl start`.

```bash
sudo jabali-isolate start <user>
```

Fails if the container has not been created yet (no rootfs exists). The container runs as a `systemd-nspawn@{user}-php` service.

---

### stop

Stop a running container via `machinectl stop`.

```bash
sudo jabali-isolate stop <user>
```

Tolerates the container already being stopped (not an error).

---

### restart

Stop then start a container.

```bash
sudo jabali-isolate restart <user>
```

Equivalent to calling `stop` followed by `start`.

---

### status

Show the current state of a container.

```bash
sudo jabali-isolate status <user>
jabali-isolate status <user> --json
```

Returns the following fields:

| Field | Description |
|---|---|
| `user` | The hosting username |
| `machine` | The systemd machine name (`{user}-php`) |
| `state` | One of: `running`, `stopped`, `missing` |
| `exists` | Whether the rootfs directory exists (`true`/`false`) |
| `enabled` | Whether the container auto-starts on boot (`true`/`false`) |

**State transitions:**

- `missing` — no rootfs exists; `create` has not been run (or `destroy` was run)
- `stopped` — rootfs exists but container is not running
- `running` — container is active (as reported by `machinectl show`)

**Plain text output:**

```
User:      user1
Machine:   user1-php
State:     running
Exists:    True
Enabled:   True
```

**JSON output (`--json`):**

```json
{
  "user": "user1",
  "machine": "user1-php",
  "state": "running",
  "exists": true,
  "enabled": true
}
```

---

### list

List all managed containers by scanning `/var/lib/machines/*-php/` directories.

```bash
sudo jabali-isolate list
jabali-isolate list --json
```

**Plain text output:**

```
USER                 MACHINE                   STATE        ENABLED
-----------------------------------------------------------------
alice                alice-php                 running      True
bob                  bob-php                   stopped      True

2 container(s)
```

**JSON output (`--json`):**

```json
[
  {"user": "alice", "machine": "alice-php", "state": "running", "exists": true, "enabled": true},
  {"user": "bob", "machine": "bob-php", "state": "stopped", "exists": true, "enabled": true}
]
```

Only directories ending in `-php` are recognized as managed containers. Other directories in `/var/lib/machines/` are ignored.

---

### destroy

Destroy a container completely: stop it if running, remove the rootfs, unit files, and socket directory.

```bash
sudo jabali-isolate destroy <user>
```

**What it does:**

1. Checks if the container is running; if so, stops it first
2. Runs `systemctl disable systemd-nspawn@{user}-php.service` to remove boot persistence
3. Removes the `.nspawn` unit file and service drop-in directory
4. Removes the rootfs at `/var/lib/machines/{user}-php/`
5. Removes the host-side socket directory at `/run/jabali-fpm/{user}/`
6. Runs `systemctl daemon-reload`

Returns silently if no container exists for the user.

---

## Features

### Container Lifecycle Management

jabali-isolator provides a complete lifecycle for per-user PHP-FPM containers:

```
create  -->  start  -->  [running]  -->  stop  -->  [stopped]  -->  destroy
                             |                          |
                             +--- restart (stop+start) -+
```

Each lifecycle operation is idempotent where possible:
- `create` rebuilds `/etc` files if the rootfs already exists
- `stop` tolerates an already-stopped container
- `destroy` tolerates a missing container (returns `false`)

**Boot persistence** is handled automatically:
- `create` runs `systemctl enable` so the container starts on every boot
- `destroy` runs `systemctl disable` before removing unit files
- The panel does not need to manage boot enablement — jabali-isolator owns it

---

### Minimal Rootfs Builder

The rootfs builder (`rootfs.py`) creates the smallest possible filesystem tree that `systemd-nspawn` needs to boot a container. No binaries are copied — all system directories are empty mount points that get populated via read-only bind mounts at runtime.

**What gets written to disk per container (~4 KB):**

| File | Contents |
|---|---|
| `etc/passwd` | Two lines: `root` + the target user (UID/GID from host) |
| `etc/group` | Two lines: `root` group + the user's primary group |
| `etc/resolv.conf` | Copied from host (resolves symlinks for systemd-resolved setups) |

**Empty directories created as mount points:**

```
tmp/           (sticky bit 1777)
run/php/       (FPM socket location inside container)
home/{user}/   (bind-mounted from host at runtime)
usr/ lib/ lib64/ bin/ sbin/   (bind-mounted read-only from host)
etc/php/       (bind-mounted read-only from host)
etc/ssl/certs/ (bind-mounted read-only for TLS)
```

The rootfs builder handles edge cases:
- **Symlinked resolv.conf** (common on systemd-resolved systems): resolves the symlink and copies the real file
- **Missing resolv.conf**: writes a fallback with `1.1.1.1` and `8.8.8.8`
- **Unknown group GID**: falls back to using the username as the group name

---

### systemd-nspawn Integration

Each container is managed as a standard `systemd-nspawn@.service` instance. jabali-isolator generates two configuration files per container:

#### `.nspawn` unit file

Located at `/etc/systemd/nspawn/{user}-php.nspawn`. Defines the container's filesystem layout and network mode.

```ini
# Managed by jabali-isolator — do not edit manually
[Exec]
Boot=no
ProcessTwo=yes

[Files]
BindReadOnly=/usr
BindReadOnly=/lib
BindReadOnly=/lib64
BindReadOnly=/bin
BindReadOnly=/sbin
BindReadOnly=/etc/php
BindReadOnly=/etc/ssl/certs
Bind=/home/{user}
Bind=/run/jabali-fpm/{user}:/run/php
TemporaryFileSystem=/tmp:mode=1777

[Network]
VirtualEthernet=no
```

Key settings:
- `Boot=no` — no init system inside the container; `ProcessTwo=yes` means the specified command runs directly as PID 2
- `BindReadOnly=` — host directories mounted read-only (binaries, libraries, PHP config, TLS certs)
- `Bind=/home/{user}` — user's home directory mounted read-write
- `Bind=...:/run/php` — socket directory bridged to host
- `TemporaryFileSystem=/tmp` — private tmpfs per container
- `VirtualEthernet=no` — shares host network stack (no veth pair)

#### Service drop-in

Located at `/etc/systemd/system/systemd-nspawn@{user}-php.service.d/limits.conf`. Sets cgroup resource limits.

```ini
# Managed by jabali-isolator — do not edit manually
[Service]
MemoryMax=512M
CPUQuota=100%
```

Both files are managed exclusively by jabali-isolator (marked "do not edit manually") and are cleaned up on `destroy`.

---

### Resource Limits (cgroups)

Each container gets its own cgroup resource limits via the systemd service drop-in:

| Limit | Setting | Default | Description |
|---|---|---|---|
| Memory | `MemoryMax` | `512M` | Hard memory cap. The kernel OOM-kills processes that exceed this. |
| CPU | `CPUQuota` | `100%` | CPU time quota per scheduling period. `100%` = 1 core, `200%` = 2 cores. |

These limits are enforced by the kernel via cgroups v2 and apply to all processes inside the container (PHP-FPM master + workers).

Set custom limits at creation time:

```bash
sudo jabali-isolate create user1 --memory 1G --cpu 200%
```

To change limits after creation, destroy and recreate the container:

```bash
sudo jabali-isolate destroy user1
sudo jabali-isolate create user1 --memory 2G --cpu 300%
sudo jabali-isolate start user1
```

---

### Mount Namespace Isolation

Each container runs in its own mount namespace. Processes inside the container see a restricted filesystem:

| Path inside container | Source | Access |
|---|---|---|
| `/home/{user}` | Host `/home/{user}` | Read-write |
| `/run/php` | Host `/run/jabali-fpm/{user}` | Read-write |
| `/usr` | Host `/usr` | Read-only |
| `/lib`, `/lib64` | Host `/lib`, `/lib64` | Read-only |
| `/bin`, `/sbin` | Host `/bin`, `/sbin` | Read-only |
| `/etc/php` | Host `/etc/php` | Read-only |
| `/etc/ssl/certs` | Host `/etc/ssl/certs` | Read-only |
| `/tmp` | Private tmpfs | Read-write |
| `/etc/passwd` | Container-local | Read-write |
| `/etc/group` | Container-local | Read-write |

Processes in the container **cannot**:
- Read other users' home directories
- Modify system binaries or libraries
- See the host's `/etc/passwd` (only root + their own user is visible)
- Access other containers' `/tmp` contents

---

### PID Namespace Isolation

Each container has its own PID namespace. Processes inside can only see:
- Their own PHP-FPM master process
- PHP-FPM worker processes spawned by the master

They **cannot**:
- See other users' processes
- Send signals to processes outside the container
- Enumerate PIDs from other containers or the host

---

### PHP-FPM Socket Bridge

PHP-FPM inside the container listens on a Unix socket. The socket directory is bind-mounted to make it accessible to the host's nginx:

```
Container: /run/php/php-fpm.sock
    |
    | (bind mount)
    v
Host: /run/jabali-fpm/{user}/php-fpm.sock
```

nginx on the host connects to the per-user socket:

```nginx
# nginx site config for user1
location ~ \.php$ {
    fastcgi_pass unix:/run/jabali-fpm/user1/php-fpm.sock;
    include fastcgi_params;
}
```

The socket directory is created during `jabali-isolate create` and removed during `jabali-isolate destroy`.

---

### Username Validation and Security

All container operations validate the username before executing any system commands. The allowed pattern is:

```
^[a-z_][a-z0-9_.-]{0,31}$
```

This means usernames must:
- Start with a lowercase letter or underscore
- Contain only lowercase letters, digits, underscores, dots, or hyphens
- Be at most 32 characters long

**Rejected inputs** (raises `IsolatorError`):

| Input | Reason |
|---|---|
| `""` | Empty string |
| `USER` | Uppercase letters |
| `1user` | Starts with digit |
| `user;rm` | Shell metacharacter `;` |
| `user$(cmd)` | Shell substitution |
| `` user`cmd` `` | Backtick execution |
| `../etc` | Path traversal |
| `user name` | Space |

All subprocess calls use **list-form arguments** (`asyncio.create_subprocess_exec`) rather than shell strings, providing a second layer of defense against injection even if validation were bypassed.

---

### JSON Output Mode

The `status` and `list` commands support `--json` for machine-readable output, enabling integration with scripts and monitoring tools.

```bash
# Single container status
jabali-isolate status user1 --json
# Output: {"user": "user1", "machine": "user1-php", "state": "running", "exists": true}

# All containers
jabali-isolate list --json
# Output: [{"user": "alice", ...}, {"user": "bob", ...}]
```

---

### Async Subprocess Execution

All system commands (`machinectl`, `systemctl`) are executed asynchronously via `asyncio.create_subprocess_exec` with:

- **List-form arguments** — no shell expansion, safe against injection
- **30-second timeout** (10 seconds for status queries) — prevents hangs from unresponsive systemd
- **Automatic cleanup** — on timeout, the subprocess is killed and waited on before raising `IsolatorError`
- **Decoded output** — stdout/stderr are decoded with `errors="replace"` to handle non-UTF-8 safely

---

### Idempotent Operations

Operations are designed to be safe to retry:

| Operation | Behavior on repeat |
|---|---|
| `create` | Rebuilds `/etc` files in existing rootfs; rewrites unit files |
| `start` | Fails if already running (machinectl error) |
| `stop` | Returns success if already stopped |
| `destroy` | Returns `false` if nothing exists |
| `rootfs build` | Overwrites `/etc/passwd`, `/etc/group`, `/etc/resolv.conf`; leaves other dirs in place |

---

## Architecture

### Module Overview

```
jabali_isolator/
  __init__.py      # Package metadata (__version__)
  __main__.py      # Click CLI — entry point for jabali-isolate command
  config.py        # Constants: paths, defaults, username regex
  rootfs.py        # Minimal rootfs builder (passwd, group, resolv.conf, directories)
  units.py         # systemd unit file generators (.nspawn + service drop-in)
  container.py     # Async orchestrator — create/start/stop/destroy/status/list
```

**Dependency graph:**

```
__main__.py (CLI)
    |
    v
container.py (orchestrator)
    |         |
    v         v
rootfs.py   units.py
    |         |
    v         v
  config.py (shared constants)
```

### Container Structure

Each user gets a minimal rootfs at `/var/lib/machines/{user}-php/`:

```
/var/lib/machines/user1-php/
  etc/
    passwd          # only root + user1
    group           # only root + user1's group
    resolv.conf     # copied from host
    php/            # mount point (bind-mounted read-only from host)
    ssl/certs/      # mount point (bind-mounted read-only from host)
  tmp/              # private tmpfs (sticky bit)
  run/php/          # FPM socket (bind-mounted to host)
  home/user1/       # mount point (bind-mounted read-write from host)
  usr/              # mount point (bind-mounted read-only from host)
  lib/              # mount point (bind-mounted read-only from host)
  lib64/            # mount point (bind-mounted read-only from host)
  bin/              # mount point (bind-mounted read-only from host)
  sbin/             # mount point (bind-mounted read-only from host)
```

No binaries are copied — host `/usr`, `/lib`, `/bin` are bind-mounted read-only. The rootfs is just a few KB of `/etc` files and empty directories.

### systemd Unit Files

Each container creates:

1. **`.nspawn` unit** at `/etc/systemd/nspawn/{user}-php.nspawn` — defines bind mounts, network, tmpfs
2. **Service drop-in** at `/etc/systemd/system/systemd-nspawn@{user}-php.service.d/limits.conf` — memory and CPU limits

Containers are managed via `machinectl start/stop` which uses the standard `systemd-nspawn@.service` template.

### Execution Flow

#### `jabali-isolate create user1 --memory 1G`

```
CLI (__main__.py)
  |-- _require_root()
  |-- container.is_available()    # checks for systemd-nspawn binary
  |-- container.create("user1", memory="1G", cpu="100%")
        |-- _validate_user("user1")
        |-- rootfs.create_rootfs("user1")
        |     |-- pwd.getpwnam("user1")        # look up UID/GID
        |     |-- mkdir /var/lib/machines/user1-php/
        |     |-- write etc/passwd, etc/group, etc/resolv.conf
        |     |-- mkdir mount-point directories
        |-- units.write_nspawn_unit("user1")    # .nspawn file
        |-- units.write_service_dropin("user1") # limits.conf
        |-- mkdir /run/jabali-fpm/user1/        # host socket dir
        |-- systemctl daemon-reload
```

#### `jabali-isolate destroy user1`

```
CLI (__main__.py)
  |-- _require_root()
  |-- container.destroy("user1")
        |-- _validate_user("user1")
        |-- container.status("user1")       # check if running
        |-- container.stop("user1")         # stop if running
        |-- units.remove_unit_files("user1") # .nspawn + drop-in
        |-- rootfs.destroy_rootfs("user1")   # rm rootfs tree
        |-- rmtree /run/jabali-fpm/user1/    # host socket dir
        |-- systemctl daemon-reload
```

---

## Isolation Details

### What's Isolated

| Resource | Inside container | On host |
|---|---|---|
| Filesystem | Only `/home/{user}` + read-only system dirs | Full filesystem |
| Processes | Only user's PHP-FPM workers | All processes |
| `/etc/passwd` | Only root + the user | All users |
| `/tmp` | Private tmpfs per container | Shared /tmp |
| Network | Shared with host (VirtualEthernet=no) | Full network |
| Memory | Capped at configured limit | Full system RAM |
| CPU | Capped at configured quota | All cores |

### What's NOT Isolated

- **Network** — shared with host via `VirtualEthernet=no`. PHP-FPM needs to serve web requests through the Unix socket bridge, and network isolation would add unnecessary complexity.
- **User IDs** — no UID remapping. Processes run as the real host UID. This simplifies file permission handling for home directory access.

---

## Panel Integration

The hosting panel calls `jabali-isolate` during user provisioning. Boot persistence is handled automatically — the panel does not need to manage `systemctl enable/disable`.

```bash
# When creating a hosting account (auto-enables boot persistence):
jabali-isolate create user1 --memory 512M
jabali-isolate start user1

# When suspending an account:
jabali-isolate stop user1

# When unsuspending an account:
jabali-isolate start user1

# When deleting an account (auto-disables and cleans up):
jabali-isolate destroy user1

# When changing PHP version — no container recreation needed!
# /etc/php is bind-mounted read-only from the host, so the container
# always sees the current PHP installation. Just restart FPM as usual.

# When changing resource limits (recreate container):
jabali-isolate destroy user1
jabali-isolate create user1 --memory 1G --cpu 200%
jabali-isolate start user1

# Monitoring / health checks:
jabali-isolate list --json
jabali-isolate status user1 --json
```

---

## Comparison with open_basedir

| | open_basedir | jabali-isolator |
|---|---|---|
| Isolation level | PHP-level | Kernel-level (namespaces) |
| Bypassed by | shell functions, FFI, opcache | Kernel bugs only |
| PID isolation | No | Yes |
| Resource limits | No | Yes (memory, CPU) |
| Filesystem | Restricts PHP file ops only | Mount namespace |
| Performance impact | Minimal | Minimal (bind mounts, no copy) |
| Disk overhead | None | ~4 KB per user (etc files) |
| `/etc/passwd` visibility | Full host passwd | Only root + target user |
| `/tmp` isolation | No | Yes (private tmpfs) |
| Signal isolation | No | Yes (PID namespace) |

---

## Development

### Running Tests

```bash
uv sync
uv run pytest
```

Tests use `tmp_path` and `monkeypatch` to redirect all filesystem operations to temporary directories — no root access or real systemd required. Async container operations are tested with `AsyncMock`.

### Project Structure

```
jabali-isolator/
  jabali_isolator/
    __init__.py         # version
    __main__.py         # Click CLI
    config.py           # paths, defaults, regex
    rootfs.py           # rootfs builder
    units.py            # systemd unit generators
    container.py        # async container orchestrator
  tests/
    __init__.py
    test_rootfs.py      # rootfs creation, destruction, edge cases
    test_units.py       # unit file generation, write/remove, existence checks
    test_container.py   # full lifecycle: create, destroy, start, stop, status, list
  pyproject.toml        # project metadata, dependencies, tool config
  uv.lock               # locked dependencies
  README.md             # this file
```

---

## License

Proprietary
