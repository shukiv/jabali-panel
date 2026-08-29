-- Reverse GH #1332 per-pool Xdebug toggle.
ALTER TABLE php_pools
  DROP COLUMN xdebug_enabled;
