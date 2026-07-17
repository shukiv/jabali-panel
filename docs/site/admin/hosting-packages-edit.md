# Edit Package

Reached from the **Edit** action on a row in [Hosting Packages](./hosting-packages.md). Modifies any field on an existing package.

## What changes propagate

A field change persists immediately to the `packages` row. The reconciler then enumerates every user assigned to the package and re-applies the relevant state:

- Disk quota changes → `setquota -u <user> <soft> <hard> 0 0` (idempotent).
- Memory / CPU / tasks changes → re-render `/etc/systemd/system/user-<UID>.slice.d/jabali-resource-limits.conf`, then `systemctl daemon-reload`.
- Request rate changes → re-render the user's nginx `limit_req_zone` directive, then reload nginx.
- PHP-INI ceilings → re-validate every user's per-user override; values exceeding the new ceiling are clamped on the user's next save (existing values remain until then to avoid silent breakage).
- Allowed PHP versions or allowed apps — informational only at edit time; the cap is re-checked at the next create attempt.

Convergence completes within 60 seconds (the reconciler tick interval).

## Caveats

- **Lowering disk quota below current usage** does not delete files. The user's writes start failing immediately; reads continue. The user must free space (or the admin must intervene) before the user can write again.
- **Lowering memory limit below current RSS** kills processes. PHP-FPM workers restart automatically; long-running user processes (cron jobs, custom daemons) do not.
- **Lowering max domains below current count** does not delete domains. The cap only applies to future create attempts.

## Field-level audit

Every save writes one audit row per changed field, with a structured diff (old value → new value). See [Audit Log](./audit-log.md).

## CLI

```bash
jabali package update <id> --memory-limit-mib 1024 --max-mailboxes 50
```

Unspecified flags retain their current values.
