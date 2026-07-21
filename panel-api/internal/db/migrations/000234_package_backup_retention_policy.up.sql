-- GH #454: admin-selectable tenant backup retention policy. When a tenant
-- reaches their package's max_backups, backup_retention_policy chooses the
-- behaviour: 'reject' (default, safe — block the new backup and notify the
-- tenant + admins) or 'prune' (auto-forget the tenant's OLDEST owned backup to
-- make room, then create, and notify). Default 'reject' preserves the existing
-- reject-at-cap behaviour so no plan changes silently until an admin opts in.
ALTER TABLE hosting_packages
  ADD COLUMN backup_retention_policy VARCHAR(16) NOT NULL DEFAULT 'reject';
