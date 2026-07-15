-- GH #429 (lxsdevcode): source panels on a non-default SSH port could not be
-- migrated — the connectors hardcoded port 22. Add a per-job source SSH port
-- (default 22) so cPanel/DirectAdmin/HestiaCP/Plesk sources behind a custom
-- port work. Schema-only; existing rows default to 22.
ALTER TABLE migration_jobs
  ADD COLUMN source_port INT NOT NULL DEFAULT 22;
