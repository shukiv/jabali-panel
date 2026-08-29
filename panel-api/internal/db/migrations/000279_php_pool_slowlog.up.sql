-- GH #1332 (item 12): per-pool FPM slow log. When > 0, FPM logs a backtrace of
-- any request slower than this many seconds to the pool's slow log (path derived
-- agent-side from the slug). 0 = disabled.
ALTER TABLE php_pools
  ADD COLUMN slowlog_timeout_seconds INT UNSIGNED NOT NULL DEFAULT 0;
