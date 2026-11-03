# ADR-0179: Moving hosts from Stalwart 0.16 to 1.0

**Status:** Accepted (2026-11-03).
**Driven by:** GH #2338 (Stalwart 1.0 is out; 0.16 gets no further releases).
**Related:** ADR-0041 / ADR-0045 (Stalwart's JMAP management API, stalwart-cli), ADR-0115 (CrowdSec reads Stalwart's log), GH #2318 (Jabali never downgrades Stalwart), JAB-216 (Stalwart's memory bound).

## Context

Stalwart 1.0 cannot open a 0.16 data store. Upstream's path is a copy: the
migration tool `stalwart-migrate` reads the 0.16 store and writes a new, empty
1.0 store. It never writes to the 0.16 store. Repeated runs copy only what
changed, so mail stops only for the final run, with 0.16 stopped. The source
must be 0.16.27.

A Jabali host keeps all of its mail in one local RocksDB store
(`/var/lib/stalwart`, named by `/etc/stalwart/config.json`). The blobs live
inside that store; there is no separate blob store.

Two things on a Jabali host can install Stalwart:
- `jabali update` runs `upgrade_stalwart_binary`, started by an admin or by the
  stable channel's auto-update.
- `install_stalwart` runs on a fresh install, and also unattended, when the
  panel's module repair runs `install.sh --install-module mail`.

On the first drill on the test box, the module repair saw Stalwart stopped for
the final copy and installed and started 0.16 in the middle of it.

The code that talks to Stalwart was made to work on both versions first:
- #2359: the CrowdSec parser. A host only gets a new parser if
  `jabali update` writes it. `provision_new_software` now runs
  `converge_crowdsec_jabali_parsers`, ahead of the migration step. Before
  that, only a fresh install or the security module install wrote Jabali's
  CrowdSec parsers. The rehearsal on the test box (0.16.24 → 1.0.2 by a real
  update) found this: the box would have kept its old parser on 1.0.
- #2360: Domain `enable`, metric names, DMARC `dateRangeStart`.
- #2361: the recipient cache flush.
- #2362: the delivery log at debug.
- #2363: the apply plan's names and the cache sizes.

## Decision

1. **One invariant: a 1.x binary never starts on a 0.16 store.** The pin is
   `STALWART_VERSION=1.0.2`. `_stalwart_next_step` routes every install
   decision:
   - An older 1.x binary gets the pin.
   - A 0.x binary older than `STALWART_SOURCE_VERSION` (0.16.27) gets 0.16.27.
   - A 0.x binary at 0.16.27 is migrated.
   - With no binary that can be read, the store decides. Empty, or stamped `1`
     in `/etc/jabali-panel/stalwart-store.generation` (written wherever a 1.x
     binary is in place), gets the pin. Unstamped data gets 0.16.27.

2. **Only `jabali update` migrates.** `upgrade_stalwart_binary` calls
   `migrate_stalwart_to_v1`. `install_stalwart` never migrates: on a 0.16
   host it brings Stalwart at most to 0.16.27 and says that the next update
   moves the data. A host is migrated only when it updates, so on the fleet
   that happens when `stable` moves.

3. **The migration** (`migrate_stalwart_to_v1`, in a subshell with its own
   traps and no exit-on-error):
   - **Checks.** `config.json` names the local RocksDB store. No copy from an
     earlier migration is in the way. There is free space for 1.5× the store
     plus 1 GB.
   - **Layout checks.** The switch renames the store directory, so the store
     must be a plain directory. A mount point (mail on its own disk, the
     layout `docs/site/mail.md` describes) can't be renamed. A symlink
     measures as nothing to `du`, so the copy would land on the disk under
     `/var/lib`. The blob store must be Stalwart's default (blobs in the data
     store): a blob store elsewhere would be named by both stores. The
     setting is read from the running 0.16 (`get BlobStore`, same answer on
     0.16.27 and 1.0.2). Each of these skips the migration: the host stays
     on 0.16.27, and the update warns.
   - **Marker and lock.** It holds `/run/jabali-installer.pid` itself (the
     update path does not), so the module repair waits. It also holds the
     Stalwart install lock.
   - **Tool and answers.** stalwart-migrate is pinned (1.0.5, checksum file).
     Jabali's answers (`install/stalwart/migrate-answers.toml`) are the tool's
     recommended ones. A question they don't answer stops `prepare
     --non-interactive`, and the host stays on 0.16.
   - **Copy and switch.** Two live `run`s while 0.16 serves. Then Stalwart
     stops for `run --final`, which copies the last changes and verifies a
     sample of accounts. Only after it completes do the stores and binaries
     switch:
     - `/var/lib/stalwart` becomes `/var/lib/stalwart-0.16`, and the copy
       becomes `/var/lib/stalwart`;
     - `/opt/stalwart` becomes `/opt/stalwart-0.16`, and the 1.0 binary
       becomes `/opt/stalwart`.

     `config.json` and the unit are unchanged.
   - **Failures.** A failure before the final copy changes nothing. A failed or
     interrupted final copy starts 0.16 again. If 1.0 does not stay up, the
     switch is undone. If 1.0 runs but answers slowly, it is left running.
   - **After the switch,** the Stalwart convergers run against 1.0.

4. **The 0.16 store and binary are kept** for a rollback. Before 1.0 has served
   users, the rollback is the switch back. After that, it is `stalwart-migrate
   rollback` into a new store. The runbook says when to remove them.

5. **Fresh installs go straight to 1.0.** The apply plan carries 1.0's names
   and is rendered for 0.x when needed. `converge_stalwart_cache_sizes` keeps
   1.0's larger caches inside the memory bound.

## Consequences

- Each fleet host's mail stops for one final copy, which took 30 s on the test
  box. The live copies before it take as long as the mail store needs, inside
  the update.
- A host without the disk space for a second copy stays on 0.16.27 and says
  so at every update until space is freed. The dual-version code keeps it
  working.
- Users of OAuth mail clients sign in again. DAV clients fetch their data
  again once. Full-text search is incomplete until 1.0 has rebuilt its index.
  Stalwart 1.0.2 resets encryption at rest to off per account; Jabali sets
  none.
- The 0.16 store and binary use disk until an admin removes them.
