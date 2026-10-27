ALTER TABLE composer_runs
  DROP KEY idx_composer_runs_user_state,
  DROP COLUMN work_dir;
DROP TABLE IF EXISTS domain_composer_settings;
