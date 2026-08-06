ALTER TABLE server_settings
  DROP COLUMN dr_last_sync_at,
  DROP COLUMN dr_last_snapshot_id,
  DROP COLUMN dr_last_sync_status,
  DROP COLUMN dr_last_sync_error;
