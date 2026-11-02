# Create Package

Reached from **Hosting Packages → Create**. Defines a new bundle of quotas and limits that may then be assigned to users.

## Required fields

- **Name** — human-readable label (e.g. "Starter", "Business", "Reseller").
- **Slug** — URL-safe identifier; cannot be changed after creation.
- **Description** — optional free text shown in the package list.

## Quotas

- **Disk quota (MiB)** — POSIX user quota on `/home/<user>`.
- **Bandwidth quota (GiB / month)** — outbound traffic cap aggregated across the user's vhosts.

## Resource limits (cgroup v2 slice)

- **Memory limit (MiB)** — wraps every process owned by the user. Setting this too low causes PHP-FPM workers to be killed under load.
- **CPU percentage** — soft limit across all CPUs (100% = one full core; 200% = two cores).
- **Tasks max** — upper bound on the number of processes the user may have running.

## Request rate (per-vhost)

- **Requests per second** — used in nginx `limit_req_zone …` for the user's vhosts.
- **Burst** — short-term allowance above the steady-state rate.

## Caps

- **Maximum domains** — refuse domain creation once exceeded.
- **Maximum mailboxes** — refuse mailbox creation once exceeded.
- **Maximum databases** — refuse database creation once exceeded.
- **Maximum cron jobs** — refuse cron creation once exceeded.

## PHP

- **Allowed PHP versions** — subset of installed PHP versions. The user's domain edit page only lists versions enabled here.
- **PHP-INI ceilings** — maximum values the user may set for `memory_limit`, `upload_max_filesize`, etc. The user's PHP Settings page clamps to these.

## Applications

- **Allowed applications** — subset of the 16-app registry the user may install one-click. Disabled apps remain installable by the admin on the user's behalf.

## Egress

- **Default-restricted** — nftables ruleset allows `:443` to anywhere plus mail submission to the panel's mail host; everything else is dropped.
- **Unrestricted** — outbound left open. Choose this only for trusted tenants.

## Save

On submit, a single `INSERT` against `packages`. No reconciler action is needed until the first user is assigned.

## CLI

```bash
jabali package create \
  --name "Starter" \
  --slug starter \
  --disk-quota-mib 5120 \
  --bandwidth-quota-gib 100 \
  --memory-limit-mib 512 \
  --cpu-pct 25 \
  --tasks-max 200 \
  --req-per-sec 30 \
  --req-burst 60 \
  --max-domains 2 \
  --max-mailboxes 10 \
  --max-databases 3 \
  --max-cron-jobs 5
```
