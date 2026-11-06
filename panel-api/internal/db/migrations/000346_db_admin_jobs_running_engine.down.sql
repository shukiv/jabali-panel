ALTER TABLE db_admin_jobs
  DROP INDEX uniq_db_admin_jobs_running_engine,
  DROP COLUMN running_engine;
