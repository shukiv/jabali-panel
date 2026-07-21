-- GH #454: schedule content + tenant scheduled-backup time (foundation).
-- backup_schedules.content: what a scheduled backup includes
--   (full/files/database/folders); default 'full' = the pre-existing behaviour,
--   so existing schedules are unchanged.
-- server_settings.tenant_backup_cron: the ADMIN-owned cron time tenant scheduled
--   backups run at. Tenants choose the content + destination; the admin owns the
--   timing (resource planning), per the GH #454 design consensus.
ALTER TABLE backup_schedules
  ADD COLUMN content VARCHAR(16) NOT NULL DEFAULT 'full';
ALTER TABLE server_settings
  ADD COLUMN tenant_backup_cron VARCHAR(64) NOT NULL DEFAULT '0 3 * * *';
