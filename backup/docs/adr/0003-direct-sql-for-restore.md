# ADR-0003: Direct SQL and useradd for restore metadata

**Date**: 2026-04-06
**Status**: accepted
**Deciders**: shuki, claude

## Context

When restoring a user account from backup, the system needs to recreate the system user, MySQL users/databases, DNS zones, email accounts, and other panel metadata. This metadata is stored in the Jabali panel's MySQL database. The CLI restorer needs to write this metadata back during restore.

The panel has its own APIs and Eloquent models, but the CLI runs as root outside the Laravel application context. Using the panel's API would create a circular dependency (backup addon calling panel API to restore data that the panel depends on).

## Decision

The CLI restore scripts write metadata directly to the Jabali panel's MySQL database using raw SQL, and create system users with `useradd`.

- `metadata.sh` creates the system user via `useradd` and inserts/updates rows in the panel's `users`, `domains`, `dns_zones`, `email_accounts`, and related tables using parameterized SQL via the `mysql` CLI client.
- `mysql.sh` restores MySQL users via `CREATE USER` / `GRANT` statements and imports database dumps directly.
- A `RESTORE_USER_CREATED` flag is exported so downstream restorers (like `files.sh`) know whether the user was freshly created or already existed.

## Alternatives Considered

### Alternative 1: Panel API calls from CLI
- **Pros**: Uses panel's validation and business logic, consistent with panel's own operations
- **Cons**: Circular dependency (addon calls panel to restore panel data), requires panel to be running during restore, authentication complexity
- **Why not**: Restore must work even when the panel is down; the addon should not depend on the panel's HTTP layer

### Alternative 2: Artisan commands in the panel
- **Pros**: Runs within Laravel context, has access to Eloquent models and validation
- **Cons**: Requires modifying the Jabali panel codebase to add restore commands, couples addon to panel internals
- **Why not**: Violates the rule that jabali-backup never modifies the Jabali panel codebase

### Alternative 3: Export/import via JSON files
- **Pros**: Decoupled, portable format
- **Cons**: Still needs something to import the JSON into MySQL, adds an intermediate format with no benefit
- **Why not**: Extra complexity with no advantage over direct SQL

## Consequences

### Positive
- Restore works independently of the panel's HTTP/application layer
- No modifications to the Jabali panel codebase
- Restore can run during panel downtime or maintenance
- Direct SQL is fast and predictable for bulk metadata operations

### Negative
- CLI must know the panel's database schema — schema changes in the panel could break restore
- Bypasses panel validation and business logic — restored data must be pre-validated
- SQL statements are tightly coupled to specific table structures

### Risks
- Panel schema migration breaks restore — mitigated by testing restore against each panel release
- Direct SQL could insert invalid data — mitigated by reading `account.json` from the backup which was originally produced by the panel's own export logic
