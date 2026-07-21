-- GH #454 Step 1: per-package tenant-backup entitlement + limits.
--
-- max_backups: tenant retention cap (max snapshots kept); 0 = tenant backups
--   NOT included on this plan (safe default, opt-in per plan — mirrors
--   max_docker_apps / max_python_apps). The whole tenant backup UI is gated on
--   max_backups > 0.
-- scheduled_backups_enabled: whether tenants on this plan may enable a
--   scheduled backup. The admin owns the schedule TIME (resource planning); the
--   tenant owns the content (what/destination) within these limits.
-- allowed_backup_destination_kinds: CSV of BackupDestinationKind* a tenant may
--   back up to (e.g. "local,s3"); empty = none allowed (must opt-in).
ALTER TABLE hosting_packages
  ADD COLUMN max_backups INT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN scheduled_backups_enabled TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN allowed_backup_destination_kinds VARCHAR(255) NOT NULL DEFAULT '';
