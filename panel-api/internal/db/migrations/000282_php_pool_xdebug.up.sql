-- GH #1332 (item 9): per-(user,version) pool Xdebug toggle (safe modes only).
-- Applied by the agent via a per-slug PHP_INI_SCAN_DIR ini.
ALTER TABLE php_pools
  ADD COLUMN xdebug_enabled TINYINT(1) NOT NULL DEFAULT 0;
