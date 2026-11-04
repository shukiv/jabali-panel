ALTER TABLE dns_zones
  DROP COLUMN last_push_error,
  DROP COLUMN last_pushed_at;
