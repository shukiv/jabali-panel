# Stalwart 0.16 → 1.0 migration

Decision record: [ADR-0179](../adr/0179-stalwart-1-0-migration.md).

`jabali update` moves a host's mail data from Stalwart 0.16 to 1.0 with
Stalwart's migration tool (`migrate_stalwart_to_v1` in `install.sh`). Mail
stops only for the final copy. This runbook covers checking the result, a
migration that stopped, a rollback, and removing the 0.16 copy.

## Where things are

| Path | What |
|---|---|
| `/var/lib/stalwart` | The live store (1.0 after the switch) |
| `/var/lib/stalwart-0.16` | The 0.16 store, kept after the switch |
| `/opt/stalwart`, `/opt/stalwart-0.16` | The 1.0 binary, and the 0.16 one kept beside it |
| `/var/lib/jabali-stalwart-migrate/` | The tool's state: `answers.toml`, `prepare-report.txt`, `final-report.txt`, `jabali-migrate.log`. Keep it while `/var/lib/stalwart-0.16` exists: `stalwart-migrate rollback` reads it |
| `/opt/stalwart-migrate/<version>/stalwart-migrate` | The migration tool |
| `/etc/jabali-panel/stalwart-store.generation` | `1` once the store is a 1.x store |

## Check a migrated host

```sh
stalwart --version                                  # 1.0.x
cat /etc/jabali-panel/stalwart-store.generation     # 1
systemctl is-active jabali-stalwart
less /var/lib/jabali-stalwart-migrate/final-report.txt
```

Then:
- sign in to webmail;
- send one mail out and one in;
- check that Mail > Logs shows "delivered".

The update's own output has a line starting with `Stalwart 1.0`.

## A migration that stopped

The update keeps going and Stalwart keeps serving 0.16. The warning says which
step stopped:

| Warning names | Meaning | Do |
|---|---|---|
| `'prepare'` | A setting needs an answer Jabali doesn't give | Read `prepare-report.txt`. Answer it in a copy of `install/stalwart/migrate-answers.toml`, or report it |
| `while copying` | A live copy failed | Read `jabali-migrate.log`. The next update resumes the copy |
| `the final copy failed` | The final copy failed; 0.16 was started again | Read `jabali-migrate.log` and `final-report.txt` |
| `MB free` | Not enough disk for a second copy | Free space, then run `jabali update` |
| `another install run` | An install was running | Run `jabali update` again later |
| `mount point or a symlink` | The store is on its own disk | See [Hosts the update doesn't migrate](#hosts-the-update-doesnt-migrate) |
| `blob store` (a type) | Blobs are kept outside the data store | See [Hosts the update doesn't migrate](#hosts-the-update-doesnt-migrate) |
| `could not read Stalwart's blob store setting` | Stalwart wasn't running, or didn't answer | Start it, then run `jabali update` |

## Hosts the update doesn't migrate

The update moves a store only in Jabali's own layout: a plain directory at
`/var/lib/stalwart`, with Stalwart's blobs inside it (the default blob
store). It checks this before it copies anything or stops mail. A host in
another layout stays on 0.16.27, mail keeps working, and every update warns
again:
- **The store is a mount point**, as in "Mail storage on another disk" in
  `docs/site/mail.md`. The switch renames `/var/lib/stalwart`, and a mount
  point can't be renamed.
- **The store is a symlink.** The free-space check would measure the link,
  and the copy would go on the disk under `/var/lib`.
- **The blob store is set to a directory, S3 or another store.** Those blobs
  are outside the copy, and both stores would name the same blob location.

Such a host can be moved by hand with `stalwart-migrate`, following upstream's
README (stalwartlabs/stalwart-migrate), with a `--target-path-map` for every
path the store uses. Moving the data back into Jabali's layout first, then
running `jabali update`, needs room for two copies of the store.

When the warning says 1.0 did not start and the host was switched back:
- Stalwart serves 0.16 again.
- The failed copy is at `/var/lib/stalwart-1.0.failed-<time>`, and its binary
  at `/opt/stalwart-1.0.failed-<time>`.
- Mail that 1.0 accepted in those seconds is in that copy.
- Later updates do not try again (they warn that a copy was switched back).

Find the cause with `journalctl -u jabali-stalwart`. To try again, move away the
failed copy and the state directory (each under its own name: the store and
the binary directories share a base name), then update:

```sh
mkdir -p /root/.trash/stalwart-1.0-failed
mv /var/lib/jabali-stalwart-migrate /root/.trash/stalwart-1.0-failed/state
mv /var/lib/stalwart-1.0.failed-* /root/.trash/stalwart-1.0-failed/store
mv /opt/stalwart-1.0.failed-* /root/.trash/stalwart-1.0-failed/bin
jabali update
```

## Rollback to 0.16

**Before 1.0 has served users** (minutes after the switch, nothing delivered),
switch back:

```sh
systemctl stop jabali-stalwart
mv /var/lib/stalwart /var/lib/stalwart-1.0.rolled-back
mv /var/lib/stalwart-0.16 /var/lib/stalwart
mv /opt/stalwart /opt/stalwart-1.0.rolled-back
mv /opt/stalwart-0.16 /opt/stalwart
ln -sfn /opt/stalwart/stalwart /usr/local/bin/stalwart
rm /etc/jabali-panel/stalwart-store.generation
systemctl start jabali-stalwart
```

**After 1.0 has served users**, the 0.16 store is stale. Copy 1.0's data back
into a new 0.16 store with `stalwart-migrate rollback`:
- Follow upstream's "Rolling back to v0.16" (the README of
  stalwartlabs/stalwart-migrate). Use a new target store, a new state
  directory, and 1.0 stopped.
- Before you start, let 1.0's queue drain.

Either way, later updates do not migrate the host again: they warn that a
completed copy was switched back. To migrate again once the cause is fixed:

```sh
mkdir -p /root/.trash/stalwart-1.0-rolled-back
mv /var/lib/jabali-stalwart-migrate /root/.trash/stalwart-1.0-rolled-back/state
mv /var/lib/stalwart-1.0.rolled-back /root/.trash/stalwart-1.0-rolled-back/store
mv /opt/stalwart-1.0.rolled-back /root/.trash/stalwart-1.0-rolled-back/bin
jabali update
```

Mail delivered to 0.16 after the rollback is copied again by that update.

## Remove the 0.16 copy

Once the host has run on 1.0 for a while (a week is plenty) and mail works,
the 0.16 store and binary are no longer a rollback path:

```sh
mkdir -p /root/.trash/stalwart-0.16
mv /var/lib/stalwart-0.16 /root/.trash/stalwart-0.16/store
mv /opt/stalwart-0.16 /root/.trash/stalwart-0.16/bin
```

Keep `/var/lib/jabali-stalwart-migrate`: it is small and records what was
copied.

## Notes

- OAuth tokens are not migrated: mail apps that signed in with OAuth sign in
  again. Jabali's webmail does not use OAuth.
- Calendar and contact (DAV) clients fetch their data again once.
- Full-text search is incomplete until 1.0 rebuilds its index after the switch.
- Stalwart 1.0.2 turns encryption at rest off per account. Jabali never turns
  it on. An account that turned it on itself has to turn it on again.
