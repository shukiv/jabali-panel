-- Reverse GH #1332 per-pool extra extensions.
ALTER TABLE php_pools
  DROP COLUMN extra_extensions;
