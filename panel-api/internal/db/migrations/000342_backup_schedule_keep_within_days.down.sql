UPDATE backup_schedules
  SET keep_daily = keep_within_days
  WHERE keep_within_days IS NOT NULL AND keep_daily IS NULL;
ALTER TABLE backup_schedules
  DROP COLUMN keep_within_days;
