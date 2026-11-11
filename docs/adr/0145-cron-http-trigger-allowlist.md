# ADR-0145: Constrained curl/wget self-domain cron http-triggers

**Date:** 2026-06-22
**Status:** Accepted
**Owner:** shuki
**Issue:** GH #400 (Part B)

## Context

jabali's cron command allowlist (`internal/cronvalidate`) is a deliberately
closed set: the first token must be `wp` or `php`, with strict `--path`/docroot
validation. It is enforced in three places — the REST/CLI intake
(`cronops`), the reconciler, and the agent's `cron.apply` (the load-bearing
enforcement point, re-validated every tick).

Migrating real cPanel accounts surfaced a large class of crons the allowlist
refuses outright: `curl`/`wget` pinging a **self-domain** URL (WordPress
`wp-cron.php`, app keep-alives, `wget --spider …/manage/get_*.php` scripts that
`require(DOCUMENT_ROOT.'/wp-load.php')`, `…/pelegapi2.php?term=…` relying on
`$_GET`). These genuinely need the web layer, so the importer's existing
`curl→php` rewrite can't convert them. They were imported **disabled** with the
original command and then could never be enabled (`cron.apply` →
`binary_not_allowed`).

GH #400 Part A (imported rows landing `enabled=1` despite intent) was already
fixed in `343f34b4` (drop GORM `default:1`, repo writes the bool verbatim,
integration test asserts disabled-import). This ADR covers **Part B**: let those
legitimate self-domain HTTP-trigger crons actually run, without opening a
generic curl-anything escape from the closed allowlist.

### Threat model (honest)

Tenants are **SFTP-only (no shell)**, so the cron system is a real exec vector —
the wp/php allowlist is a genuine boundary, not cosmetic. **However**, a tenant
`php` cron already has unrestricted outbound HTTP today: on the live host
`allow_url_fopen=On` and `disable_functions` is empty, so a tenant can write a
`.php` file via SFTP and schedule `php x.php` doing `file_get_contents()` /
`exec('curl …')` to anywhere — including `169.254.169.254` and RFC1918.

Therefore curl/wget self-domain triggers grant **no new network reach** over the
status quo, and this work does **not** close an SSRF hole — the php path remains
wide open (a separate, pre-existing concern: empty `disable_functions`). The
guards below are **defense-in-depth + least-surprise**: keep the curl/wget
surface minimal, not a new security boundary.

The M34 per-user egress firewall is **not** an SSRF control here: it is opt-in
and its canonical default allowlist *accepts* loopback + RFC1918
(`user_egress_template.go`, for MariaDB/Redis), so it cannot be relied on to
block internal targets.

## Decision

A **separate, equally-closed** validator + a **rebind-safe runtime wrapper** —
the wp/php validator is never widened.

### Layer 1 — static validator (`cronvalidate.ValidateHTTPTrigger`, pure, no I/O)

Routed to by `ValidateAny` when the first token's basename is `curl`/`wget`.
Accepts only when **all** hold:

- control characters rejected (same gate as wp/php — a newline would break the
  single-quoted `ExecStart` token and inject a systemd directive);
- exactly one `http(s)://` URL; scheme `http`/`https`; no userinfo; ports 80/443
  only; **query strings allowed** (unlike the importer's php-rewrite, since the
  real HTTP request honours `$_GET`);
- URL host is one of **this account's own domains** (exact match, no subdomain
  widening);
- closed benign-flag allowlist (`-s --silent -f --fail -q --spider -nv
  --no-verbose`, plus `-o/-O/--output-document` only as `/dev/null`); everything
  else (`-o file`, `-T`, `-d/--data`, `-x/--proxy`, `-L`, `-e`, …) is rejected.

Returns `Command{Kind: "http_trigger", URL, Argv: ["/usr/local/bin/jabali",
"cron", "http-trigger", url]}`. The Argv is the **wrapper invocation**, so both
`cron.apply` (systemd `ExecStart`) and `cron.run_now` (`systemd-run -- …`) emit
it uniformly with no special-casing.

### Layer 2 — runtime guard (`jabali cron http-trigger <url>`)

The systemd unit's `ExecStart` is this command, **not** the raw curl/wget. It
runs as the tenant uid (DB-free, no `PreRunE`). It:

1. resolves the host (A + AAAA);
2. rejects if **any** resolved address is non-routable —
   `cronvalidate.IsBlockedIP`: loopback, unspecified, link-local unicast
   (169.254/16 incl. the 169.254.169.254 metadata endpoint, `fe80::/10`),
   multicast, RFC1918 + ULA (`net.IP.IsPrivate`). Deliberately **not**
   `IsGlobalUnicast()` (true for RFC1918);
3. pins the validated IP(s) via `curl --resolve host:port:ip`, so curl never
   re-resolves — closing the TOCTOU DNS-rebind window between check and connect;
4. issues a fixed hardened GET: `--proto =http,https --max-redirs 0 --max-time
   30 -sS -f -o /dev/null`. Always GET (not `--spider`/HEAD — these endpoints do
   work on access, and HEAD risks not triggering the job).

Collapsing both curl and wget to one hardened `curl` at exec removes wget's
flag/`wgetrc` quirks from the runtime path.

### Plumbing

`owned_domains` is threaded alongside `owned_docroots` everywhere a command is
validated: REST/CLI intake (`cronops.ownedTargets`), reconciler
(`ownedTargetsFor`), agent `cron.apply` + `cron.run_now` params. The agent is
the enforcement point; REST/CLI are UX.

The importer is unchanged: self-domain curl/wget that can't be `php`-rewritten
is still imported **disabled** with the original command (Part A safety gate +
operator review). The new capability is that enabling such a row now validates
(`ValidateHTTPTrigger`) and applies as an http-trigger unit instead of failing.

## Consequences

- The 6 real self-domain crons from the example-i.co.il migration become runnable
  once the operator reviews + enables them; cross-domain (e.g. `example-n.co.il`)
  stays blocked (`http_foreign_host`).
- No new outbound reach vs the existing php-cron path; the guard keeps the
  curl/wget surface minimal and rebind-safe.
- `curl` is a hard runtime dependency of the wrapper — already guaranteed
  (installer bootstrap + it is the installer's own fetch tool).
- Tests: per-rejected-case validator table, `IsBlockedIP` table (every private
  range + positives), `ValidateAny` routing, control-char rejection on the curl
  path, the http-trigger `ExecStart` shape, and the runtime reject paths
  (bad scheme / non-web port / loopback host).

## Out of scope / follow-up

- Empty `disable_functions` (tenant php → arbitrary shell/outbound as their uid)
  is a pre-existing, broader hardening item, not part of #400.
