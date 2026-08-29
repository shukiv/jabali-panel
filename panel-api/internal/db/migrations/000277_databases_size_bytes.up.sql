-- GH #1242: persist each tenant database's on-disk size so the admin User List
-- can show total storage = home quota + DB + mail. The panel-api DB user is
-- scoped to its own schema and cannot read tenant sizes from information_schema,
-- so a sweeper asks the root agent (db.usage.by_schema) and writes size_bytes
-- back here. `databases` is a MariaDB reserved word — backtick it.
ALTER TABLE `databases`
  ADD COLUMN `size_bytes` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN `size_checked_at` DATETIME(6) NULL DEFAULT NULL;
