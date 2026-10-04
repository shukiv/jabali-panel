# Security

Layered. CrowdSec is the IP-trust source; UFW handles the port baseline; AppSec WAF replaces ModSecurity; Snuffleupagus + AppArmor harden the application layer; AIDE watches the host; per-user egress firewall stops compromised tenants from being usable for outbound abuse.

## CrowdSec — single source of IP-trust (M43)

ADR-0089. CrowdSec is the only thing that decides whether an IP gets blocked.

- **Bouncers**: nginx (rate-limit + AppSec inspect), Stalwart (SMTP/IMAP), Bulwark, sshd.
- **Scenarios**: HTTP probe/scan, SSH bruteforce, IMAP/SMTP auth flood, app-specific WP/Drupal scan, malware-upload attempt.
- **Decisions**: BAN, CAPTCHA, ALLOWLIST.
- **Console**: enrol via `/jabali-admin/security` → CrowdSec → Console; central per-org view at `app.crowdsec.net`.

UFW is **demoted**: only port-open/port-close baseline. Old `ufw deny from <ip>` rules are migrated into CrowdSec decisions by `jabali ufw migrate-ip-bans`.

CrowdSec extensions (M27, ADR 0061-0063):
- **Per-IP allowlists** — admin-managed, persists across CrowdSec restarts.
- **Per-scenario override** — change a scenario's severity / leakspeed / capacity at admin level.
- **Alert routing** — a burst of new bans fires the `crowdsec.ban.spike` notification.

## AppSec WAF (M27 — replaces ModSecurity)

ADR-0060. ModSecurity is **removed** (M27 cleanup_modsecurity purges packages + configs every install; migration 000074 drops the schema). Replacement is **CrowdSec AppSec**:

- Inline `appsec-block` bouncer in nginx (`/etc/nginx/conf.d/jabali-appsec.conf`).
- Rule packs from `hub.crowdsec.net/author/crowdsecurity` (vpatch family for CVE virtual-patching).
- AppSec install path is **flat** (`/etc/crowdsec/appsec-rules/`) — no `crowdsecurity/` subdir (the install-path scar that purged + reinstalled 170 vpatch rules every update, fixed in PR #69).

### AppSec bot detection (CrowdSec 1.8)

A CrowdSec 1.8 AppSec **bot-detection challenge** is available, **off by default**
(per-server). It is opt-in and layered:

- **Per-server**: admins enable it under `/jabali-admin/security` → CrowdSec /
  AppSec (default **OFF**).
- **Per-domain opt-in** (`scope=selected`): turn the challenge on for chosen
  domains only.
- **Per-domain opt-out**: exclude a specific domain from the challenge.
- **Tenant self-service**: a tenant can toggle bot detection on **their own**
  domains without an admin.

Because it is default-off and per-domain scoped, enabling it never silently
challenges traffic on a domain the operator didn't choose.

### WAF false positives and rule exclusions

`/jabali-admin/security` → CrowdSec → **WAF exclusions** (GH #1649) answers
"which rule blocked this?" and turns that rule off for one host and path:

- **Recent WAF blocks** inspects the last 10, 25 or 50 AppSec alerts and
  groups their blocks by rule, host and path. It loads only when you click
  **Load blocks**, because each alert is inspected on the server.
  - Red rules scored and can be excluded.
  - Purple rules are outside the CRS range, for example CrowdSec's own AppSec
    rules, so an exclusion cannot turn them off.
  - Grey rules (901340, 949110, 949111, 980170) appear on almost every block and are
    never the one to exclude.
  - Many source IPs on one path usually means a false positive. One IP across
    many paths usually means an attack.
- **Rule exclusions** lists, adds and removes operator exclusions. An
  exclusion always names a host, a path prefix and one rule. **Exclude…** on a
  block prefills all three. Every add and remove applies to the WAF at once.
  If crowdsec cannot be reloaded, the change is undone and the error says so.
- A row tagged **Flarum** is managed for a Flarum forum (GH #1650). Removing it
  works, but the next change to that forum or `jabali appsec flarum-sync` adds
  it again.

The same data is on the CLI: `jabali appsec explain` and
`jabali appsec exclusion add|list|rm`. The CLI stores the row only; run
`jabali appsec render-config --reconcile --reload` to apply it.

## AppArmor

`/jabali-admin/security` → AppArmor — per-profile status (enforce / complain / **missing**).

Jabali ships and manages these daemon profiles: `jabali-panel` (panel API), `jabali-agent`, `jabali-bulwark` (webmail), `stalwart-mail`, and `jabali-fpm-app` (GH #690 — the per-user PHP-FPM/WordPress workload profile, attached to fpm-exec; ships complain-first for the soak, flip to enforce per-host after soak-readiness shows 0 would-deny). In enforce mode `jabali-fpm-app` lets tenant PHP start only the shell (`sh`/`bash`/`dash`), `cat` and the `mail()` shim, so `exec()`/`shell_exec()` calls to other programs (`df`, `ls`, `grep`, `id`, …) fail with "Permission denied" even when the hosting package allows those functions; the domain's PHP Settings page marks them **Starts only the shell and cat** (GH #2001). While the profile is in complain mode or not loaded, an allowed exec function can run any program the site's user can run (shell access, for whoever takes over the site); PHP Settings then marks it **Can start any program**, and the package editor warns as soon as a package allows one. Nothing else limits it: PHP Defense lifts its exec ban for packages that allow exec. A profile that fails to load or is purged is reported as **missing** (red) rather than silently omitted — an absent profile means an unconfined daemon.

New profiles ship in **complain** mode for a 7-day burn-in soak; each profile shows a **soak-readiness** indicator (complain-mode profiles with zero would-deny events are ready to flip to enforce). Complain-mode `apparmor="ALLOWED"` would-deny events are surfaced alongside enforce-mode `DENIED` denials, so a complain-mode profile actively logging violations is not mistaken for a clean state. A daily timer **auto-promotes** a soak-clean profile from complain to enforce (JAB-349), so hardening advances without a manual flip on every host.

A **degraded-AppArmor** condition (a managed profile that should be enforcing but
isn't) raises a dashboard alert plus a runtime confinement smoke test (JAB-379),
so a silently-unconfined daemon is caught rather than assumed safe.

On kernels with broken unix-socket mediation (missing `/sys/kernel/security/apparmor/features/unix`), Jabali deliberately does **not** load the daemon profiles — on fresh install and on `jabali update` alike.

The panel API daemon holds **no** AppArmor policy-management capability (`mac_admin`): mode flips are delegated to `panel-agent`, so a compromised panel cannot disable its own confinement.

## Snuffleupagus

PHP runtime hardening, built for every installed PHP version and loaded into PHP-FPM and the PHP command line. The mode is **off** by default; **simulation** logs what would be stopped and **enforce** stops it. The loaded rules block the command-execution functions (`system`, `exec`, `shell_exec`, `passthru`, `popen`, `proc_open`, `pcntl_exec`), `assert`, `show_source`, `highlight_file` and `phpinfo`, and harden sessions (SameSite cookie, signed `unserialize()` data, XXE protection). More rules ship in the bundle but are not loaded yet; see [PHP Defense](./admin/snuffleupagus.md).

## AIDE host-integrity

A daily timer (`jabali-aide-check.timer`) compares the host against the AIDE database. Changes outside the panel's drop-in paths fire the `aide.tamper.detected` notification.

## Per-user egress firewall (M34)

ADR-0084. nftables + cgroup v2 vmap. Each user's processes run in their slice; the nftables ruleset matches by cgroup ID and decides:

- Allow `:443` to anywhere (HTTPS — legitimate API use).
- Allow `:587/465/993` to the panel's own mail host (so PHP scripts can submit mail).
- Drop everything else by default.

**External database ports are dropped by default** (GH #638). The default
allowlist is web + mail only (`25, 53, 80, 443, 465, 587`); outbound to a
**remote** database — MSSQL `1433`, MySQL/MariaDB `3306`, PostgreSQL `5432`,
Redis `6379`, MongoDB `27017`, etc. — is dropped by the enforced chain. This is
intentional (a compromised tenant can't exfiltrate to or pivot through an
arbitrary remote DB), but it also blocks a *legitimate* app that connects to an
external managed database. A dropped connection surfaces in the panel under
**Users → Edit → Egress** (recent drops feed) so the operator can see *why* a
tenant's outbound DB connection is failing rather than guessing.

To allow a legitimate external DB, add the port (and optionally the destination
CIDR) to that user's egress allowlist under **Users → Edit → Egress**. Prefer a
CIDR-scoped rule (the specific DB host) over opening the port to `0.0.0.0/0`.

Admin overrides per-user under Users → Edit → Egress.

**Every hosting user is enrolled.** The reconciler gives each user without a
policy a row on every tick. On a host installed before the egress firewall
existed, the row starts in **learning**: blocked connections are logged
(`journalctl -k | grep jabali-egress-learn-<user>`) but allowed. After 7 days
the nightly timer switches it to **enforced**. On newer hosts it starts
enforced. Watch **Users → Edit → Egress** during those 7 days, and add the
extras a tenant's app needs. To hold every learning user in learning, run
`echo learning > /etc/jabali/per-user-egress.pin`. The
`/etc/jabali/per-user-egress.mode` file only picks the starting state; it is
not a pin.

**SSH shells are covered too.** An SSH login does not run in the user's slice:
logind places it in `user.slice/user-<uid>.slice/session-N.scope`. The ruleset
therefore also matches by socket owner (`meta skuid`), after the cgroup match,
so a shell and every command started from it goes through the same allowlist
and the cloud-metadata floor. Only connections the tenant opens outward are
affected; inbound SSH, SFTP, `scp` and `rsync` to the server are not. This means
`ssh`/`git@github.com` out of a shell needs the package's **SSH out** allowance
(`egress_ssh_out`), and `ping` needs **ICMP** (`egress_icmp`), exactly as for
PHP. Only uids from 1000 up are matched, so system daemons are never filtered.

## Malware (M33, M33.2)

- **Linux Malware Detect (LMD) 2.0** — native HEX, MD5 and SHA-256 scanner with the rfxn signature pack. It replaced ClamAV, which was removed in M33 (amendment 3).
- **LMD real-time monitor** — `jabali-maldet-monitor.service`, opt-in (default off, mig 000082); apply-then-persist toggle.
- **YARA-X** (`yr`) — pattern matching for LMD: the rfxn rule pack, the signature-base (Neo23x0) rules, and rules the admin uploads.
- **M33.2 mail-yara-async** (ADR-0079) — async post-delivery JMAP-poll YARA scan; NOT MtaHook/MtaMilter.
- **Quarantine-rate circuit breaker** (JAB-248) — the scanner trips a breaker if the quarantine rate spikes, so a bad signature can't quarantine a tenant's whole tree in a runaway loop.

## Additional hardening

- **Step-up auth for high-risk admin tools** (JAB-380) — the admin File Manager
  and Root Terminal require a fresh (recent-auth / MFA) re-authentication before
  they open, so a stolen session alone can't reach a root shell or the whole
  filesystem.
- **API-side write allow-list for the admin File Manager** (JAB-367) — writes are
  checked against an allow-list on the API, not just the UI, so a crafted request
  can't write outside the permitted paths.
- **CrowdSec pprof/metrics locked down** (JAB-368) — tenant-uid processes are
  blocked from CrowdSec's unauthenticated `:6060` pprof / metrics endpoint.
- **Package disk quota on tenant database storage** (JAB-243) — a tenant's DB
  storage counts against the hosting-package disk quota, so databases can't be
  used to bypass the account quota.
- **Country exemption from local MaxMind** (PR #1083) — country-based
  allow/exempt CIDRs are derived from a local MaxMind mmdb plus supplemental
  CIDRs, with no external lookup at request time.
- **Secret-rotation tooling** (JAB-357) — `jabali secrets rotate <name>`
  (operator-only) rolls the panel DB app-user password, the Redis panel token,
  and the PowerDNS DB password live (each with its own verify + rollback), plus
  the now-vestigial `JWT_SECRET`, so a leaked credential can be rolled without a
  reinstall. Transient migration source-host credentials are **purged, not
  rotated**. See [../secret-rotation.md](../secret-rotation.md).

## CrowdSec test-IP card

`/jabali-admin/security` → CrowdSec Test IP — paste any IPv4/IPv6, see whether CrowdSec would block / captcha / allow it right now, with the matching decision row.

## Audit log

`/jabali-admin/audit`. Append-only (ADR-0106). Every privileged mutation:
- Who (subject user, actor user, source).
- What (action, target).
- When.
- Result (ok / fail).
- Diff (where applicable).

CLI: `jabali audit query --q db.root` searches the log; `jabali audit verify` checks its hash chain.
