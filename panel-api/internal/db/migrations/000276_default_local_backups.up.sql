-- GH #1240: opt-in automatic daily local backups for all users.
--
-- default_local_backups_enabled (server-wide, default OFF — local backups are an
-- opt-in the admin turns on at install or in Server Settings; the operator
-- prefers remote and dislikes local by default). When ON, a single admin-owned
-- managed schedule (backup_schedules.is_managed_default) backs up every tenant to
-- the local repo daily; when OFF it's disabled and tenant-created schedules are
-- untouched.
--
-- is_managed_default marks THAT one panel-managed schedule so the provisioner can
-- find + converge it idempotently without touching admin/tenant schedules.
--
-- Both TINYINT(1) — 1 byte each, no server_settings row-size (ERROR 1118) concern.
ALTER TABLE server_settings
    ADD COLUMN default_local_backups_enabled TINYINT(1) NOT NULL DEFAULT 0;

ALTER TABLE backup_schedules
    ADD COLUMN is_managed_default TINYINT(1) NOT NULL DEFAULT 0;

-- Enforce AT MOST ONE managed default schedule: a concurrent create race (two
-- panel instances, or a CLI tick vs the 60s tick) must not produce duplicate
-- all-tenants schedules. A virtual column that is 1 only for the managed row and
-- NULL otherwise, with a unique index (NULLs are not unique in MariaDB), permits
-- exactly one is_managed_default=1 row; the loser's INSERT gets a dup-key error
-- the provisioner treats as "someone else won".
ALTER TABLE backup_schedules
    ADD COLUMN managed_default_uk TINYINT GENERATED ALWAYS AS (IF(is_managed_default = 1, 1, NULL)) VIRTUAL,
    ADD UNIQUE KEY uk_backup_schedules_managed_default (managed_default_uk);
