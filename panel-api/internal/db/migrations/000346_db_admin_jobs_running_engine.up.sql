-- One maintenance run per engine, held by the database (ADR-0100). The panel
-- and the `jabali db maintenance` CLI both check for a running job before they
-- insert one; this key makes the insert itself refuse a second running job for
-- the same engine. The app sets running_engine = engine while a job runs and
-- clears it to NULL when the job finishes; NULLs never collide. Additive and
-- nullable: every existing row stays NULL.
ALTER TABLE db_admin_jobs
  ADD COLUMN running_engine VARCHAR(16) NULL,
  ADD UNIQUE KEY uniq_db_admin_jobs_running_engine (running_engine);
