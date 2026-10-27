# Security

Layered. CrowdSec is the IP-trust source; UFW handles the port baseline; AppSec WAF replaces ModSecurity; Snuffleupagus + AppArmor harden the application layer; AIDE watches the host; per-user egress firewall stops compromised tenants from being usable for outbound abuse.

## CrowdSec — single source of IP-trust

CrowdSec is the only thing that decides whether an IP gets blocked.

- **Bouncers**: nginx (bans, CAPTCHA, AppSec WAF) and the nftables firewall bouncer, which drops banned IPs on every port, so mail (SMTP, IMAP, POP3) and SSH are covered too.
- **Scenarios**: HTTP probe/scan, SSH bruteforce, IMAP/SMTP auth flood, app-specific WP/Drupal scan, malware-upload attempt.
- **Decisions**: BAN, CAPTCHA, ALLOWLIST.
- **Blocklist**: the free CrowdSec community blocklist (CAPI), plus any extra lists the admin turns on (FireHOL levels 1–3, IPsum level 3, Spamhaus DROP for IPv6, or the admin's own list URLs). The panel does not use the CrowdSec console; enrolment was removed. See [Firewall → Blocklists](firewall.md#blocklists).

UFW is **demoted**: only port-open/port-close baseline. Old `ufw deny from <ip>` rules are migrated into CrowdSec decisions by `jabali ufw migrate-ip-bans`.

CrowdSec extensions:
- **Per-IP allowlists** — admin-managed, persists across CrowdSec restarts.
- **Per-scenario override** — change a scenario's severity / leakspeed / capacity at admin level.
- **Alert routing** — a burst of new bans fires the `crowdsec.ban.spike` notification.

## AppSec WAF (replaces ModSecurity)

ModSecurity is **removed** (cleanup_modsecurity purges packages + configs every install; migration 000074 drops the schema). Replacement is **CrowdSec AppSec**:

- Inline `appsec-block` bouncer in nginx (`/etc/nginx/conf.d/jabali-appsec.conf`).
- Rule packs from `hub.crowdsec.net/author/crowdsecurity` (vpatch family for CVE virtual-patching).
- AppSec install path is **flat** (`/etc/crowdsec/appsec-rules/`) — no `crowdsecurity/` subdir (an earlier bug purged and reinstalled 170 vpatch rules on every update; it is fixed).

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

### Blocking bots

Several layers turn bots away. The first four are on for every site without
any setting:

- **CrowdSec bad user agents** (`crowdsecurity/http-bad-user-agent`): a client
  whose User-Agent is on CrowdSec's list of several hundred known bad agents
  (scanners, attack tools, some crawlers such as MJ12bot, DotBot and PetalBot)
  gets its address banned on every site.
- **CrowdSec behaviour scenarios**: clients that crawl too fast
  (`http-crawl-non_statics`), probe for missing or sensitive paths
  (`http-probing`, `http-sensitive-files`) or scan WordPress are banned, whatever
  user agent they send.
- **Community blocklist**: addresses other CrowdSec users reported (see
  [Firewall → Blocklists](firewall.md#blocklists)).
- **AppSec WAF**: requests that look like attacks are refused.

Off by default:

- **AppSec bot detection** (above): a JavaScript / proof-of-work challenge for
  visitors that look like bots. Verified search engines and AI crawlers pass
  it, so it does not stop AI crawlers.
- **Block AI crawlers**, per domain: the site answers 403 to the crawlers and
  assistants of AI companies that name themselves in the User-Agent header
  (GPTBot, ClaudeBot, CCBot, PerplexityBot, Bytespider and others). The tenant
  turns it on on the domain's **Overview** tab; the admin also has it under
  Admin → Domains → Edit. No Google or Bing crawler is on the list, Google's
  AI fetchers included. Google's and Apple's AI training opt-outs (`Google-Extended`,
  `Applebot-Extended`) are robots.txt tokens, not user agents, so a site that
  wants those adds them to its `robots.txt`.
- **Blocked user agents**, server-wide: Admin → Security → CrowdSec → Bot
  Detection → **Blocked user agents**. Every site answers 403 to a request
  whose User-Agent contains one of the entries (one per line, case does not
  matter, up to 200). Unlike the CrowdSec list, nothing is banned: each
  matching request is refused. The refusal comes before any other rule,
  Let's Encrypt validation included, so an entry that is part of a
  browser's or Let's Encrypt's user agent, or that would match any Google or
  Bing crawler (such as `compatible`, `Mozilla`, `bot`, `AdsBot` or `Google`),
  is not accepted. Google and Bing are never blocked. An entry saved before a
  newer release protected the crawler it matches is left out on its own, and
  the rest of the list still applies.
- **Rate limits**, per domain (admin only): see
  [Resource limits](resource-limits.md#3-nginx-request-and-connection-limits-per-domain).

The two user-agent blocks live in `/etc/nginx/conf.d/00-jabali-bots.conf`.
A site's vhost refers to them only when that site uses them, so with both off
the vhosts are unchanged.

A user-agent block stops only a bot that says who it is. A bot that sends a
browser's user agent is caught, if at all, by the CrowdSec scenarios, the WAF
or the bot-detection challenge.

### WAF false positives and rule exclusions

`/jabali-admin/security` → CrowdSec → **WAF exclusions** answers
"which rule blocked this?" and turns that rule off for one host and path:

- **Recent WAF blocks** inspects the last 10, 25 or 50 AppSec alerts and
  groups their blocks by rule, host and path. It loads only when you click
  **Load blocks**, because each alert is inspected on the server.
  - Red rules scored and can be excluded.
  - Purple rules are outside the CRS range, for example CrowdSec's own AppSec
    rules, so an exclusion by id cannot turn them off. A CrowdSec virtual
    patch can be excluded by its name instead (below).
  - Grey rules (901340, 949110, 949111, 980170) appear on almost every block and are
    never the one to exclude.
  - Many source IPs on one path usually means a false positive. One IP across
    many paths usually means an attack.
- **Rule exclusions** lists, adds and removes operator exclusions. An
  exclusion always names a host, a path prefix and one rule. **Exclude…** on a
  block prefills all three. Every add and remove applies to the WAF at once.
  If crowdsec cannot be reloaded, the change is undone and the error says so.
- The rule is a CRS rule id (`942100`) or the name of a CrowdSec virtual patch
  or generic AppSec rule (`crowdsecurity/vpatch-CVE-2025-29927`). A virtual
  patch's block is an alert of its own, named after it: the scenario in the
  CrowdSec **Alerts** tab, or in `cscli alerts list`, is the name. `crowdsecurity/crs` and
  `crowdsecurity/base-config` cannot be excluded: that would turn the WAF off
  for the path.
  - An exclusion by rule id is matched against the request URI as the client
    sent it, query string included. A URI with a `..` path segment, plain or
    percent-encoded (`%2e%2e`), never matches, because nginx may serve it
    outside the prefix. A `..` inside a name, as in `report..final.pdf`, still
    matches. The built-in WordPress and DokuWiki exclusions have the same guard.
  - An exclusion by name is matched against the decoded path, without the
    query string. A path that contains `..`, or a request URI that starts with
    `//`, never matches.
  - It goes into `/etc/crowdsec/appsec-configs/jabali-appsec.yaml`, and the
    new config is checked with `crowdsec -t` before the reload.
  - If no installed rule has the name, the exclusion is saved and the card says
    it does nothing until a rule of that name is installed.
- A row tagged with an app name is managed for that app's install:
  - **Flarum**: rule 920450 on the forum's API path. Removing it
    works, but the next change to that forum or `jabali appsec flarum-sync`
    adds it again.
  - **Nextcloud**: rule 911100 on the install's four WebDAV
    paths (`/remote.php/dav/`, `/remote.php/webdav/`, `/public.php/dav/`,
    `/public.php/webdav/`), so the Files app, sync clients and calendar/contact
    sync can use WebDAV methods. Removing one blocks those clients again.

  The panel removes a managed row when its install is deleted, and at start-up
  for any install that is gone.

The same data is on the CLI: `jabali appsec explain` and
`jabali appsec exclusion add|list|rm`. The CLI stores the row only; run
`jabali appsec render-config --reconcile --reload` to apply it.

## AppArmor

`/jabali-admin/security` → AppArmor — per-profile status (enforce / complain / **missing**).

Jabali ships and manages these daemon profiles: `jabali-panel` (panel API), `jabali-bulwark` (webmail), `stalwart-mail`, and `jabali-fpm-app` (the per-user PHP-FPM/WordPress workload profile, attached to fpm-exec; ships complain-first for the soak, flip to enforce per-host after soak-readiness shows 0 would-deny). In enforce mode `jabali-fpm-app` lets tenant PHP start only the shell (`sh`/`bash`/`dash`), `cat` and the `mail()` shim, so `exec()`/`shell_exec()` calls to other programs (`df`, `ls`, `grep`, `id`, …) fail with "Permission denied" even when the hosting package allows those functions; the domain's PHP Settings page marks them **Starts only the shell and cat**. Its explicit deny rules hold in complain mode too: tenant PHP can't read `/etc/shadow`, `/root` or any account's `.ssh`, and can't connect to the system D-Bus. While the profile is in complain mode or not loaded, an allowed exec function can run any program the site's user can run (shell access, for whoever takes over the site); PHP Settings then marks it **Can start any program**, and the package editor warns as soon as a package allows one. Nothing else limits it: PHP Defense lifts its exec ban for packages that allow exec. A profile that fails to load or is purged is reported as **missing** (red) rather than silently omitted — an absent profile means an unconfined daemon.

`jabali-sendmail` confines the PHP `mail()` shim: `jabali-fpm-app` hands the shim to it whenever a site's PHP sends mail. It ships in its own profile file, so its mode is switched on its own: an admin can keep `jabali-fpm-app` in complain (so `df`, `ls` and the like work from PHP) and still enforce `jabali-sendmail`. `jabali update` keeps the mode each profile is loaded in, so a server where the shim was enforced along with `jabali-fpm-app` keeps it enforced. With a smarthost selected for website mail, the profile also lets the shim read the mode file and connect to the website mail relay's socket; the relay itself runs as its own `jabali-mailrelay` user under a systemd sandbox (no capabilities, read-only system, no `/home`). The daily auto-promotion timer (below) doesn't switch `jabali-fpm-app` or `jabali-sendmail`; an admin does.

The phpMyAdmin and Adminer PHP pool (`jabali-pma`, user `www-data`) does not run under `jabali-fpm-app`: its `jabali-fpm@pma` unit starts php-fpm directly. `www-data` can read every site's files, and a program started from PHP ignores `open_basedir`, so the pool disables the command-exec functions instead. It disables the same functions as the tenant lockdown (`exec`, `passthru`, `shell_exec`, `system`, `proc_open`, `popen`, `pcntl_exec`, `pcntl_fork`, `proc_nice`, `dl`) plus `putenv`, `mail` and `mb_send_mail`, which close the `LD_PRELOAD`-plus-sendmail way around a disabled `exec`. Neither app needs these functions to serve a page. The only phpMyAdmin code that could use them on a page is the **External** transformation, and phpMyAdmin ships its list of allowed programs empty, so it already runs nothing. `jabali update` applies the list to existing servers.

New profiles ship in **complain** mode for a 7-day burn-in soak; each profile shows a **soak-readiness** indicator (complain-mode profiles with zero would-deny events are ready to flip to enforce). Complain-mode `apparmor="ALLOWED"` would-deny events are surfaced alongside enforce-mode `DENIED` denials, so a complain-mode profile actively logging violations is not mistaken for a clean state. A daily timer **auto-promotes** a soak-clean profile from complain to enforce, so hardening advances without a manual flip on every host. A profile kept in complain mode on purpose can be **acknowledged** on the AppArmor page: the Server Status alert then leaves it out until its mode changes. A missing or unconfined profile is always reported.

A **degraded-AppArmor** condition (a managed profile that should be enforcing but
isn't) raises a dashboard alert plus a runtime confinement smoke test,
so a silently-unconfined daemon is caught rather than assumed safe.

On kernels with broken unix-socket mediation (missing `/sys/kernel/security/apparmor/features/unix`), Jabali deliberately does **not** load the daemon profiles — on fresh install and on `jabali update` alike.

The panel API daemon holds **no** AppArmor policy-management capability (`mac_admin`): mode flips are delegated to `panel-agent`, so a compromised panel cannot disable its own confinement.

`jabali update` overwrites the profile files. Site-specific rules go in `/etc/apparmor.d/local/<profile file>`, which every profile includes and Jabali never writes; see [AppArmor → Custom rules](./admin/apparmor.md#custom-rules).

## Snuffleupagus

PHP runtime hardening, built for every installed PHP version and loaded into PHP-FPM and the PHP command line. The mode is **off** by default; **simulation** logs what would be stopped and **enforce** stops it. The loaded rules block the command-execution functions (`system`, `exec`, `shell_exec`, `passthru`, `popen`, `proc_open`, `pcntl_exec`), `assert`, `show_source`, `highlight_file` and `phpinfo`, and harden sessions (SameSite cookie, signed `unserialize()` data, XXE protection). More rules ship in the bundle but are not loaded yet; see [PHP Defense](./admin/snuffleupagus.md).

## AIDE host-integrity

A daily timer (`jabali-aide-check.timer`) compares the host against the AIDE database. Changes outside the panel's drop-in paths fire the `aide.tamper.detected` notification.

Security → AIDE runs a check, or rebuilds the baseline, in the background; the page shows each while it runs and says why a rebuild failed. See [AIDE](./admin/aide.md).

## Per-user egress firewall

nftables + cgroup v2 vmap. Each user's processes run in their slice; the nftables ruleset matches by cgroup ID and decides:

- Allow `:443` to anywhere (HTTPS — legitimate API use).
- Allow `:587/465/993` to the panel's own mail host (so PHP scripts can submit mail).
- Drop everything else by default.

**External database ports are dropped by default**. The default
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
existed, the row starts in **learning**: each new connection the allowlist
would block is allowed and logged (`journalctl -k | grep jabali-egress-learn-<user>`),
and Security → Egress lists it as **Allowed (learning)**. After 7 days
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

**App ports on loopback belong to their account.** Python apps, Docker apps
and reverse-proxy domains listen on a `127.0.0.1` port that nginx proxies to.
Only the account that owns the app, and system users such as nginx and root,
can open a connection to that port. A connection from any other account is
dropped, whatever that account's egress state, from PHP, cron jobs and SSH
shells alike. So nginx stays in front of every app: its WAF, password
protection and IP rules apply to every request that reaches the app. Ports the
panel did not allocate are not affected, so a service an account runs on a port
of its own keeps working. An administrator's own login (uid 1000 and up) is
treated like any other account, so reach an app port from a shell as root
(`sudo curl http://127.0.0.1:<port>/`). The drops are counted in
`nft list counter inet jabali_per_user loopback_app_drops`.

A Python app's port also answers only from the app. A new connection to it is
accepted only when the process listening on the port runs in the owner's slice,
where the panel runs the app; any other listener gets nothing, and the drops
are counted in `loopback_app_squat_drops`. A slice created again (a new cgroup)
is picked up on the next reconciler pass, within a minute.

## Malware

- **Linux Malware Detect (LMD) 2.0** — native HEX, MD5 and SHA-256 scanner with the rfxn signature pack. It replaced ClamAV, which was removed.
- **LMD real-time monitor** — `jabali-maldet-monitor.service`, opt-in (default off, mig 000082); apply-then-persist toggle.
- **YARA-X** (`yr`) — pattern matching for LMD: the rfxn rule pack, the signature-base (Neo23x0) rules, and rules the admin uploads.
- **mail-yara-async** — async post-delivery JMAP-poll YARA scan; NOT MtaHook/MtaMilter.
- **Quarantine-rate circuit breaker** — the scanner trips a breaker if the quarantine rate spikes, so a bad signature can't quarantine a tenant's whole tree in a runaway loop.

## Additional hardening

- **Step-up auth for high-risk admin tools** — the admin File Manager
  and Root Terminal require a fresh (recent-auth / MFA) re-authentication before
  they open, so a stolen session alone can't reach a root shell or the whole
  filesystem.
- **API-side write allow-list for the admin File Manager** — writes are
  checked against an allow-list on the API, not just the UI, so a crafted request
  can't write outside the permitted paths.
- **CrowdSec pprof/metrics locked down** — tenant-uid processes are
  blocked from CrowdSec's unauthenticated `:6060` pprof / metrics endpoint.
- **Package disk quota on tenant database storage** — a tenant's DB
  storage counts against the hosting-package disk quota, so databases can't be
  used to bypass the account quota.
- **Country exemption from local MaxMind** — country-based
  allow/exempt CIDRs are derived from a local MaxMind mmdb plus supplemental
  CIDRs, with no external lookup at request time.
- **Installer credentials on stdin** — the one-click app installers hand the
  database and admin passwords to each app's installer on stdin, never as
  command-line options or environment variables of the `systemd-run` unit it
  runs in. Each transient unit's description names only its binary, so command
  lines stay out of the journal.
- **Secret-rotation tooling** — `jabali secrets rotate <name>`
  (operator-only) rolls the panel DB app-user password, the Redis panel token,
  and the PowerDNS DB password live (each with its own verify + rollback), plus
  the now-vestigial `JWT_SECRET`, so a leaked credential can be rolled without a
  reinstall. Transient migration source-host credentials are **purged, not
  rotated**. See [../secret-rotation.md](../secret-rotation.md).

## CrowdSec test-IP card

`/jabali-admin/security` → CrowdSec Test IP — paste any IPv4/IPv6, see whether CrowdSec would block / captcha / allow it right now, with the matching decision row.

## Audit log

`/jabali-admin/audit`. Append-only. Every privileged mutation:
- Who (subject user, actor user, source).
- What (action, target).
- When.
- Result (ok / fail).
- Diff (where applicable).

CLI: `jabali audit query --q db.root` searches the log; `jabali audit verify` checks its hash chain.
