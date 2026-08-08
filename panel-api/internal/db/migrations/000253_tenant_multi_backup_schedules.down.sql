ALTER TABLE backup_schedules DROP COLUMN cadence;
ALTER TABLE server_settings
  DROP COLUMN tenant_backup_window_start,
  DROP COLUMN tenant_backup_window_end;
ALTER TABLE hosting_packages DROP COLUMN max_backup_schedules;
