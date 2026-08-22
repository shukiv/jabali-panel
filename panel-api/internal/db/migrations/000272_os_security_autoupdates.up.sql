-- JAB-353: OS security auto-upgrades secure-by-default.
--
-- The Updates Center shipped apt_enabled defaulting to FALSE, so an
-- Internet-facing panel could sit with unattended-upgrades disabled and known
-- OS security fixes unapplied for months. Fix:
--   (1) record an intentional opt-out (apt_optout_acknowledged) so a deliberate
--       "off" is distinguishable from the old silent insecure default;
--   (2) force-enable existing hosts whose apt_enabled=0 came from that silent
--       default — no acknowledgement column existed, so any current 0 was never
--       an operator decision;
--   (3) extend update_state with the OS-patch status the page now reports
--       (last applied time, reboot-required).
-- Fresh installs seed apt_enabled=TRUE via the app's EnsureDefault (data seeds
-- live in the app, not migrations — the 000057 "Dirty database" scar).
--
-- Statement order: both DDL (ALTER) statements FIRST, the DML (UPDATE) LAST.
-- go-sql-driver + golang-migrate run the whole file as one multi-statement
-- Exec; a DML that is not the final statement (an UPDATE followed by more DDL)
-- fails the batch, so the UPDATE must come after every ALTER.

ALTER TABLE update_autoupdate_config
  ADD COLUMN apt_optout_acknowledged BOOLEAN NOT NULL DEFAULT FALSE AFTER apt_enabled;

ALTER TABLE update_state
  ADD COLUMN apt_last_applied_at TIMESTAMP NULL              AFTER apt_checked_at,
  ADD COLUMN apt_reboot_required BOOLEAN   NOT NULL DEFAULT FALSE AFTER apt_last_applied_at;

-- Force-enable existing hosts: their 0 predates the acknowledgement flag, so it
-- was the silent default, not an operator opt-out. (On a fresh install the
-- singleton row does not exist yet — it is seeded enabled by EnsureDefault — so
-- this UPDATE simply affects 0 rows there.)
UPDATE update_autoupdate_config SET apt_enabled = TRUE WHERE apt_optout_acknowledged = FALSE;
