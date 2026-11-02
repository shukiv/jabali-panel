# WHM Migration

The WHM ingest path. Status: production-supported; effectively a batch of cPanel restores.

## Source archive

A WHM-level dump produced by:

```bash
/scripts/pkgacct <user>
# or whole-server (one cpmove per account):
/scripts/cpbackup
```

The output is one or more `cpmove-<user>.tar.gz` files plus a WHM manifest that maps accounts to packages and resellers.

## How the pipeline runs

The WHM ingest:

1. Reads the manifest.
2. Maps each WHM "package" to the closest existing [Hosting Package](./hosting-packages.md) (matching by disk quota and bandwidth quota). Operator confirms or overrides the mapping.
3. For each user, runs the [cPanel pipeline](./cpanel-migration.md) with the chosen target package.
4. Reports per-user success / failure plus a server-wide summary.

Resellers are flattened: each reseller-owned account becomes a panel user with no parent-child relationship (the panel has no reseller construct).

## Operator workflow

1. Generate the WHM dump on the source server.
2. Upload to the destination — large dumps go via SCP into `/var/lib/jabali/migrations/incoming/`.
3. **Analyze** the dump. Review the package mapping; create new panel packages if the suggested mapping is wrong.
4. **Restore**. The pipeline restores the dump account by account.
5. After completion, review the per-account failures (if any) and re-run individual cPanel pipelines for them.

## Bandwidth-staging notes

- Producing a multi-GiB WHM dump and shipping it over a slow link is the slowest part of the migration. Stage the dump on a local NVMe disk first; transfer with `rsync --partial --progress` for resumability.
- For very large estates (dozens of GiB), consider migrating in waves: produce + transfer + restore one batch of accounts, repeat. Avoids holding the source under cutover load for hours.

## Limitations

Inherits all [cPanel pipeline](./cpanel-migration.md) limitations, plus:

- Reseller branding (white-label themes, custom logos) does not carry over.
- WHM-level cron jobs (`/etc/cron.d/*` outside per-user crontabs) are not migrated.
- WHM "Tweak Settings" do not map to panel server settings; review and re-apply manually under [Server Settings](./server-settings.md).

## Audit

One per-user restore audit row plus per-phase rows per user.
