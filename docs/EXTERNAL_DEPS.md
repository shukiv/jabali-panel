# External Dependencies

Every third-party component install.sh pulls in, with its pinned version
(when one exists) and where the pin lives in the codebase. Keep this file
in lockstep with the install script — CI does not yet lint for drift but
operators read this to know what they own at install time.

When a row says **distro**, the version is whatever the OS apt
repository ships at install time. Set policy with the apt source we add
(Sury for PHP, packagecloud for CrowdSec, etc.) — pinned majors land
here, the minor patches roll forward freely.

When a row lists an explicit upstream version, search install.sh for
the string to bump it.

---

## Runtime + build toolchain

| Component | Version / pin | Source | install.sh anchor | Why |
|---|---|---|---|---|
| Go toolchain | `1.26.4` (override: `JABALI_GO_VERSION`) | go.dev tarball | `GO_VERSION="${JABALI_GO_VERSION:-1.26.4}"` (line ~122) | builds panel-api + panel-agent |
| PHP (Sury / ondrej) | distro-latest from `packages.sury.org` (Debian) or `ppa:ondrej/php` (Ubuntu) | apt | sury+ondrej repo blocks (line ~1538) | tenant PHP runtime, multi-version per-user pools |

## Web / proxy / TLS

| Component | Version | Source | Notes |
|---|---|---|---|
| nginx | **distro main** — Sury's nginx repo was dropping the package per `feedback_nginx_debian_native_not_sury`; install.sh purges sury nginx defensively | apt | M0 |
| certbot | distro | apt | M32 LE for panel hostname + tenant domains |

## Database

| Component | Version | Source | Notes |
|---|---|---|---|
| MariaDB | distro 11.x | apt | panel DB + tenant DBs (M7). Reserved-word trap on 11.4+ documented in `feedback_mariadb_reserved_words` |
| Redis | distro | apt | M14 notifications stream + SSO session store |
| Adminer | `6.0.1` | `github.com/vrana/adminer` release | M37 DB UI |
| phpMyAdmin | `5.2.3` | phpmyadmin.net tarball | M7 |

## DNS

| Component | Version | Source | Notes |
|---|---|---|---|
| PowerDNS Authoritative | distro | apt | tenant + panel zones |
| PowerDNS Recursor | distro | apt | M6.3 split-port loopback resolver; zz-jabali-recursor drop-in |

## Security

| Component | Version | Source | Notes |
|---|---|---|---|
| CrowdSec engine | packagecloud + distro main | apt | M27 + M43 IP-trust single source |
| CrowdSec nginx bouncer | distro | apt | inline ban |
| CrowdSec AppSec hub | hub-pinned | `cscli hub install` | M27, custom vpatch rules at `/etc/crowdsec/appsec-rules/` |
| Snuffleupagus (PHP ext) | `0.13.0` | source build | M41 PHP hardening rules |
| ClamAV | distro **binary only** — `clamd` + `freshclam` daemons masked | apt | M33 on-demand scan; signatures refresh via `jabali-freshclam.timer` |
| YARA-X | `1.17.0` | `github.com/VirusTotal/yara-x` release tarball | M33 malware engine; clamscan subset constraints per `feedback_clamscan_yara_subset` |
| LMD (Linux Malware Detect) | `2.0.1-rc4` | upstream tar | M33 signature feed |
| Tetragon | upstream packagecloud (opt-in) | apt | M33 runtime detect (sessionwatcher ingestion) |
| AIDE | distro | apt | file-integrity baseline |

## Mail (M6)

| Component | Version | Source | Notes |
|---|---|---|---|
| Stalwart mail server | `0.16.7` | `github.com/stalwartlabs/stalwart` release | SMTP + IMAP + JMAP per ADR-0041 |
| Stalwart CLI | `1.0.8` | `github.com/stalwartlabs/cli` release | mail admin |
| Stalwart spam-filter rules | version from upstream sha file | `github.com/stalwartlabs/spam-filter` | rule pack |
| Bulwark webmail | `1.7.3` | `github.com/bulwarkmail/webmail` release | M6 webmail; per-mailbox SSO |

## Identity (M20)

| Component | Version | Source | Notes |
|---|---|---|---|
| Ory Kratos | `26.2.0` | `github.com/ory/kratos` release tarball | sole auth source after M20 cutover (ADR-0034 amended) |

## Containers (M48 opt-in)

| Component | Version | Source | Notes |
|---|---|---|---|
| Docker CE + docker-compose-plugin | distro-latest | `download.docker.com/linux/{debian,ubuntu}` per `$ID` from `/etc/os-release` | M48 marketplace; opt-in via Server Settings → Apps |

## App management

| Component | Version | Source | Notes |
|---|---|---|---|
| restic | distro | apt | backups (M30) |
| wp-cli | `2.12.0` | `github.com/wp-cli/wp-cli` release | M10 WordPress install/clone |

---

## Decommissioned (do not re-add)

| Component | Removed in | Reason |
|---|---|---|
| Hydra (OIDC) | M16 rollback (ADR-0036 superseded by ADR-0038) | M22 magic-link replaced OIDC consent |
| filebrowser | M11 (ADR-0030) | AntD-native page at `/jabali-panel/files` |
| ModSecurity | M27 (ADR-0055 superseded by ADR-0060) | CrowdSec AppSec covers the WAF surface |

---

## Update workflow

For an **upstream-versioned pin** (Kratos, Stalwart, YARA-X, etc.):

1. Search install.sh for the version literal.
2. Change the string. Re-read the upstream release notes for breaking
   changes.
3. Update the test bench: a fresh VM with `jabali install --debug` and
   the existing `tests/scripts/post-install-smoke.sh`.
4. Update this file's row.
5. PR.

For a **distro-tracked package** (nginx, MariaDB, certbot):

1. Distro repo controls the floor. Patch-level updates land via
   `apt-get upgrade` / `unattended-upgrades`.
2. When the distro bumps a major (e.g. MariaDB 11.4 → 11.8) and breaks
   anything, file a memory note in
   `~/.claude/projects/-home-shuki-projects-jabali2/memory/` so the next
   migration round picks it up.
