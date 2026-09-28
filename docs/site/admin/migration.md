# Migration

`/jabali-admin/migrations`. Parent page for the cPanel / DirectAdmin / Hestia / WHM ingest pipelines. Each source has its own subpage:

- [cPanel migration](./cpanel-migration.md)
- [DirectAdmin migration](./directadmin-migration.md)
- [HestiaCP migration](./hestiacp-migration.md)
- [WHM migration](./whm-migration.md)

## Pipeline shape (shared)

Every source uses the same four-phase pipeline:

1. **Analyze** — inspect the uploaded archive, list users, domains, databases, mailboxes, DNS zones, cron jobs. No write yet.
2. **Fix-perms** — chown / chmod normalisations to match Jabali's per-user pool layout.
3. **Validate** — DB password hashes parseable, DNS zone files valid, mail account schemas consistent.
4. **Restore** — create the panel user, ingest each domain, database, mailbox, cron job. Hand off to the reconciler for vhost render and per-user state convergence.

Each phase logs to a structured per-job journal viewable from `/jabali-admin/migrations/<id>`.

## Page surface

- **Incoming archives** — files uploaded but not yet started.
- **In-flight jobs** — currently running; per-row progress percentage + current phase.
- **Completed jobs** — succeeded or failed; click for full per-stage log.

## Upload paths

- Web upload: drag-and-drop on the page.
- SCP: drop the archive into `/var/lib/jabali/migrations/incoming/` and refresh the page.

The web upload chunks large files; SCP is faster for >1 GiB archives over slow links.

## Stop-the-world semantics

Each pipeline run targets a single destination user. While that user's domains, databases, and mailboxes are being created, panel-side writes against the same user (UI or CLI) are queued and applied after the run completes. Reads remain available.

## After-the-fact tidy

After a successful restore:

```bash
jabali domain prune-orphans            # list nginx sites with no panel row
jabali domain prune-orphans --apply    # delete them
```

Catches domains the source had soft-deleted but the cpmove archive still referenced.

## Limitations

- **No live migration.** Each pipeline is offline relative to the destination user.
- **No Plesk** — the Plesk backup format is not currently supported.
- **No CSF / LFS rule translation.** Carry over allowlists manually into [CrowdSec Allowlists](./crowdsec-allowlists.md).
