ALTER TABLE hosting_packages
  DROP COLUMN allowed_backup_destination_kinds,
  DROP COLUMN scheduled_backups_enabled,
  DROP COLUMN max_backups;
