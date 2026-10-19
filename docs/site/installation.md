# Installation

## Supported platforms

- **Debian 13 (Trixie)** — primary, fully tested.
- Earlier Debian / Ubuntu — installer detects and warns; not supported.

## Requirements

| Resource | Minimum | Recommended |
|---|---|---|
| RAM | 2 GB | 4 GB+ |
| Disk | 20 GB | 40 GB+ SSD |
| CPU | 1 vCPU | 2 vCPU+ |
| Network | Public IPv4, ports 22/25/53/80/443/465/587/993/995 open | + outbound 53/udp+tcp |
| OS | Debian 13 fresh install | — |

The RAM guidance matches cPanel: **2 GB is the minimum, 4 GB is recommended.** The installer builds the panel (SPA + Go binaries) on the box, so on a ≤4 GB host it provisions a swap file and caps the build's memory (RAM-aware V8 heap + serialized `go build`) so compiling doesn't get OOM-killed — on 2 GB the build is slower but completes. Baseline services (MariaDB, CrowdSec) auto-size down on small hosts; on a very tight 2 GB box you can install without the `security` module to drop CrowdSec's ~500 MB WAF engine.

If outbound `:53/udp` is blocked (some labs), set `JABALI_DNS_FORWARDER=<a-reachable-resolver>` so the recursor forwards over TCP.

## Install

```bash
curl -fsSL https://get.jabali-panel.com | bash
```

The installer is idempotent — safe to re-run. It writes drop-ins, never overwrites manual edits to host configs outside of the files it owns.

### Environment variables

| Variable | Effect |
|---|---|
| `JABALI_DNS_FORWARDER=<ip>` | Force pdns-recursor to forward `.` to this resolver (use when outbound :53/udp is blocked). Installer also rewrites `/etc/resolv.conf` to point at the same IP and masks systemd-resolved. |
| `JABALI_PANEL_HOSTNAME=<fqdn>` | Pre-seed Server Settings → Panel Hostname so the Let's Encrypt cert and Stalwart primary mail domain land on the right FQDN from first boot. |
| `JABALI_SKIP_*` (various) | Skip individual install steps. Generally avoid — supported only for debugging. |

## Choosing modules

The core always installs: nginx with PHP-FPM, MariaDB, Redis, the panel, its agent, and Kratos for sign-in. Everything else is a module you choose.

**Interactive install.** The one-line install opens the installer. Pick a profile, then adjust the module list:

| Profile | Modules |
|---|---|
| Minimal | none |
| Web host | `dns`, `security`, `quota` |
| Mail server | `dns`, `mail`, `security` |
| Full | every module |
| Custom | starts from Web host |

**Unattended install.** Set `JABALI_MODULES` to a comma list of module keys. The installer then skips its screens and installs that list:

```bash
curl -fsSL https://get.jabali-panel.com | JABALI_MODULES=dns,mail,security bash
```

Left unset, `JABALI_MODULES` gives the Web host profile. Set but empty, it gives Minimal.

| Key | Installs |
|---|---|
| `dns` | PowerDNS: authoritative DNS and per-domain records. |
| `mail` | Stalwart mail server and Bulwark webmail. Needs `dns`, which is added when missing. |
| `security` | CrowdSec, malware scanning (Linux Malware Detect, YARA-X), the UFW firewall and AppArmor. |
| `docker` | Docker engine and the admin's 1-click Docker apps. |
| `docker_apps` | Docker apps for hosting users. Needs `docker`, which is added when missing. It doesn't work in an unprivileged container. |
| `python_apps` | The runtime for users' Python apps. |
| `postgres` | PostgreSQL alongside MariaDB. |
| `quota` | Per-user disk quotas. |
| `api` | The REST API and its keys. |

To see what a selection installs without changing anything, add `--dry-run`:

```bash
curl -fsSL https://get.jabali-panel.com | JABALI_MODULES=dns,mail,security bash -s -- --dry-run
```

If `docker`, `docker_apps` or `postgres` fails to install, the installer warns and finishes the rest. That module's switch in the panel stays off, so you can turn it on there later.

**After the install.** Server Settings → Modules turns `dns`, `mail`, `security`, `quota` and `api` on or off. Docker, Docker apps for users and Python apps are under Server Settings → Apps; PostgreSQL is under Server Settings → Databases. From the shell, `install.sh --install-module KEY` installs `dns`, `mail`, `security`, `quota` or `ftp` on an installed server.

## What the installer does

1. Verifies Debian 13, root, no conflicting cPanel/Plesk/CyberPanel install.
2. Adds Sury PHP repository (PHP 8.1–8.5+) and PackageCloud for CrowdSec.
3. **Purges sury-nginx** if present — Jabali uses Debian-native nginx (Sury dropped nginx in 2026; this is defensive).
4. Installs system packages: nginx, mariadb, php-fpm (all current Sury versions), redis, certbot, restic, ufw. The modules add their own: pdns-server and pdns-recursor for `dns`; Stalwart for `mail`; CrowdSec, Linux Malware Detect (LMD) and YARA-X for `security`, which also sets up the UFW firewall; Docker for `docker`; postgresql for `postgres`.
5. Installs Kratos, and with `mail` Bulwark (Node SPA bridge), on Unix sockets — no TCP `:4433/:4434/:3000` exposure.
6. Builds `jabali-panel-api` (Go) and `jabali-agent` (Go), writes systemd units, starts services.
7. Runs DB migrations.
8. Issues an admin one-time-login URL.

## Post-install

```bash
jabali repair --diagnose   # 7-detector self-heal report
jabali update              # pulls latest code, rebuilds, migrates, reloads
```

Diagnostics:

```bash
journalctl -u jabali-panel.service -f
journalctl -u jabali-agent.service -f
jabali system diagnostic   # upload an encrypted support bundle; prints a link and a password
```

## Uninstall

There is no `--uninstall` flag. The intended teardown is to redeploy the VM. If you must keep the host, the manual path is documented in [troubleshooting.md](./troubleshooting.md#full-teardown).

## Known good install paths

- KVM / QEMU on a clean Debian 13 cloud image — works.
- LXC unprivileged — not supported (cgroup v2 host requirements + systemd-user timers fail in unprivileged containers).
- LXD privileged on a recent host — works.
- Docker / nspawn — not supported as the panel host (the panel itself uses systemd-machined for some operations).
