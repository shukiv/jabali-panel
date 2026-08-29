-- GH #1332 (item 16): per-(user,version) pool opt-in extra PHP extensions,
-- loaded via php_admin_value[extension] in the pool conf. JSON array of names.
ALTER TABLE php_pools
  ADD COLUMN extra_extensions JSON NULL DEFAULT NULL;
