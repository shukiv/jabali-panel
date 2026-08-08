-- GH #331 DR standby, Step 2: standby pull-restore liveness state.
--
-- The drsync loop (panel-api/internal/drsync) runs only on a standby: each tick
-- it lists the primary's system_backup manifests on the DR destination and, if a
-- newer one exists than it last applied, runs `system.restore {apply:true,
-- include_accounts:false}` to converge the panel DB + config + TLS. These columns
-- record the outcome so `jabali dr status` (and, later, the admin banner) can show
-- how fresh the replica is.
--
-- dr_last_sync_at: when the loop last completed a tick (any outcome).
-- dr_last_snapshot_id: the system_backup manifest snapshot this box last applied
--   (restic short/long id). Empty until the first successful restore.
-- dr_last_sync_status: '' (never run) | ok (applied a new snapshot) | current
--   (already at newest) | waiting (destination has no manifest yet) | error.
-- dr_last_sync_error: last error string (truncated), empty on success.
ALTER TABLE server_settings
  ADD COLUMN dr_last_sync_at     DATETIME(6)   NULL,
  ADD COLUMN dr_last_snapshot_id VARCHAR(64)   NOT NULL DEFAULT '',
  ADD COLUMN dr_last_sync_status VARCHAR(16)   NOT NULL DEFAULT '',
  ADD COLUMN dr_last_sync_error  VARCHAR(1024) NOT NULL DEFAULT '';
