-- GH #1332 (items 8/11): per-domain PHP runtime directives, rendered as
-- fastcgi_param PHP_VALUE alongside the existing resource-limit overrides.
--   php_display_errors  — pinned on every PHP vhost by the agent (default Off).
--   php_error_reporting — int bitmask (0 = report nothing; E_ALL = 32767).
--   php_timezone        — date.timezone tz-database identifier.
-- All NULL = use the pool default. The `domains` table is not a reserved word.
ALTER TABLE domains
  ADD COLUMN php_display_errors TINYINT(1) NULL DEFAULT NULL,
  ADD COLUMN php_error_reporting INT NULL DEFAULT NULL,
  ADD COLUMN php_timezone VARCHAR(64) NULL DEFAULT NULL;
