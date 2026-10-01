-- GH #1817: drop the retired per-user webmail toggle, users.webmail_enabled
-- (GH #316, migration 000203).
--
-- GH #1628 slice 3 (#1760) removed every reader and #1763 removed every
-- writer. The column was kept as slice 3's rollback net until a stable
-- release carried both: the 2026-10-01 release (7715b0dc5) does. A box
-- reaches this migration only with a binary that has both changes, so
-- nothing reads the column any more. Migration 000300 reads it, but runs
-- before this one.
ALTER TABLE users DROP COLUMN webmail_enabled;
