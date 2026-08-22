-- Reverse JAB-353 schema additions. The force-enable data change is not
-- reverted (there is no record of the prior per-host value, and re-disabling
-- security updates on down-migrate would be the wrong default anyway).
ALTER TABLE update_state
  DROP COLUMN apt_reboot_required,
  DROP COLUMN apt_last_applied_at;

ALTER TABLE update_autoupdate_config
  DROP COLUMN apt_optout_acknowledged;
