-- GH #1701: per-package disabled PHP functions.
--   php_disabled_functions — the php_admin_value[disable_functions] list the
--                            package's pools run with, comma-separated.
-- NULL = the GH #401 command-exec lockdown, or no list at all when the row's
-- php_exec_enabled is set: the panel reads a NULL list through
-- php_exec_enabled, so existing opt-outs keep working without a backfill, and
-- an older binary that flips php_exec_enabled on a NULL row stays authoritative.
-- TEXT, not VARCHAR, keeps the row clear of the InnoDB row-size ceiling.
ALTER TABLE hosting_packages
  ADD COLUMN php_disabled_functions TEXT NULL DEFAULT NULL;
