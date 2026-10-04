# ADR-0146: Tenant PHP command-exec lockdown + always-on cloud-metadata egress floor

**Date:** 2026-06-22
**Status:** Accepted — live-verified on 10.0.3.14
**Owner:** shuki
**Issue:** GH #401

## Context

Surfaced while implementing #400 (the cron curl/wget allowlist). Tenant
PHP-FPM pools shipped with **empty `disable_functions`** and
**`allow_url_fopen=On`**, and the per-user pool template set `open_basedir`
(ADR-0023) but nothing restricting process exec or outbound network.
`open_basedir` confines *filesystem* access only — `exec`/`shell_exec`/
`proc_open` spawn external processes that escape the jail, and sockets/HTTP are
unaffected. Tenants are SFTP-only (no shell), so PHP (web request or cron) is
their exec vector. Net effect: any tenant could run arbitrary OS commands and
make arbitrary outbound connections **as their own uid**, including reaching the
cloud-metadata endpoint `169.254.169.254` (IAM-credential theft on a cloud VPS)
and internal services (SSRF).

The M34 per-user egress firewall does not mitigate this: it is opt-in and its
canonical default allowlist *accepts* loopback + RFC1918 (for MariaDB/Redis),
and `169.254.0.0/16` is reachable on a default install.

## Decision

Two independent, defense-in-depth layers.

### 1. Command-exec lockdown (`disable_functions`)

The per-user pool template (`install/php/jabali-php-pool.conf.tmpl`) now
hard-codes:

```
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl
```

`php_admin_value` (not tenant-overridable) and in the agent's
`forbiddenDirectives` set, exactly like `open_basedir`. Blocks **process
spawning only** — `curl_exec`/`file_get_contents`/`fsockopen` stay enabled
because WordPress + most apps need the HTTP layer; outbound SSRF is handled by
layer 2, not by amputating HTTP.

Propagation to EXISTING tenants: `ReconcilePHPPools` only (re)applies
missing/pending/error pools, so a template edit never reaches active pools on
its own. New CLI `jabali php pool reapply-all` flips every active pool to
`pending` so the next reconciler tick re-renders it from the current template;
`jabali update` runs it after syncing the template. (A future, broader option
is to make the pool path converge every tick like the domain-vhost path; out of
scope here.)

### 2. Always-on cloud-metadata / link-local egress floor

`RenderEgressNFT` now emits, in the `output` chain **before** the per-user vmap
dispatch and independent of per-user enrollment:

```
socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip  daddr 169.254.0.0/16 counter name ssrf_floor_drops drop
socket cgroupv2 level 2 "jabali.slice/jabali-user.slice" ip6 daddr fe80::/10       counter name ssrf_floor_drops drop
```

`level 2` = the tenant parent slice, so it matches ANY process in any
`jabali-user-<u>.slice` (where PHP-FPM runs), enrolled or not. Emitted only when
the parent slice exists (nft verifies cgroupv2 paths at load).

**Latent bug fixed in passing:** the nft file emitted a bare
`table inet jabali_per_user { … }` and reloaded with `nft -f` (no flush), so the
`output` chain's rules **accumulated** a duplicate `vmap` line on every
reconcile reload (harmless for idempotent vmap, but it reordered the new floor
after the per-user dispatch). The file now opens with `add table` +
`delete table` + redefine, an atomic flush-and-rebuild in one transaction.

## Consequences

- Tenant `shell_exec`/`exec`/`proc_open` return "disabled"; `curl`/HTTP intact.
  Verified via the FPM socket: `shell_exec_exists=no`, `curl_exists=yes`.
- A tenant FPM process to `169.254.169.254` is dropped (counter increments);
  public `:443` still works. Verified live (drop counter 0→4, `https://…`=200).
- The egress table no longer accumulates duplicate rules across reloads.
- `curl` (#400 wrapper) + metadata floor are independent; either alone reduces
  the surface, together they cover app-level and kernel-level.

## Not done (follow-up)

- IPv6 ULA metadata (`fd00:ec2::254`) is not blocked — would require dropping
  all `fc00::/7` for tenants, which could break legitimate v6 ULA use; revisit
  if v6 metadata becomes a concern.
- ~~Per-package `disable_functions` opt-out~~ — **shipped (GH #402)**:
  `hosting_packages.php_exec_enabled` (admin-only, default 0). When set, the
  reconciler sends `disable_functions=""` for that package's pools so the agent
  emits no lockdown line. `disable_functions` is now spec-driven
  (`defaultDisableFunctions` const, single source) — the template renders
  `{{.DisableFunctions}}`, not a hard-coded list. The tenant override guard
  (`forbiddenDirectives`) is unchanged, so only an admin-assigned package can
  flip it. Package edits fan out a pool re-render.
  **GH #1701:** the opt-out is now a per-package list,
  `hosting_packages.php_disabled_functions` (NULL = this lockdown), with
  `php_exec_enabled` derived from it. In PHP Defense enforce mode the opt-out
  used to do nothing, because 00-base.rules bans the same command-execution
  functions in every pool; a pool whose list allows any of them now loads its
  own copy of the PHP Defense rules without those bans.
