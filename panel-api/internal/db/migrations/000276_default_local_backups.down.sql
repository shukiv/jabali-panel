-- Revert GH #1240 default-local-backups opt-in.
ALTER TABLE backup_schedules
    DROP KEY uk_backup_schedules_managed_default,
    DROP COLUMN managed_default_uk;
ALTER TABLE backup_schedules DROP COLUMN is_managed_default;
ALTER TABLE server_settings DROP COLUMN default_local_backups_enabled;
