-- GH #2300: an account's "Days to keep" keeps every backup from the last N
-- days, counted back from the newest complete backup (restic's
-- --keep-within). It was keep_daily, which keeps only the newest backup of
-- each day, so a schedule that runs every 6 hours with 7 days to keep held 7
-- restore points instead of 28.
--
-- keep_within_days is that rule. An account's own schedule (user_id set) moves
-- its keep_daily there; admin schedules (user_id NULL, the managed default
-- local schedule included) keep their keep_daily/weekly/monthly. For a daily
-- schedule both rules keep the same backups.
ALTER TABLE backup_schedules
  ADD COLUMN keep_within_days INT NULL AFTER keep_monthly;

UPDATE backup_schedules
  SET keep_within_days = keep_daily, keep_daily = NULL
  WHERE kind = 'account_backup' AND user_id IS NOT NULL AND keep_daily IS NOT NULL;
