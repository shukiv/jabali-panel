-- Reverse GH #1332 per-pool FPM slow log.
ALTER TABLE php_pools
  DROP COLUMN slowlog_timeout_seconds;
