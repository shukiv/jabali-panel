-- idx_composer_runs_user_state starts with user_id, so InnoDB let it take
-- over from the index it made for fk_composer_runs_user and dropped that
-- one. Put a user_id index back before dropping it.
ALTER TABLE composer_runs
  ADD KEY fk_composer_runs_user (user_id);
ALTER TABLE composer_runs
  DROP KEY idx_composer_runs_user_state,
  DROP COLUMN work_dir;
DROP TABLE IF EXISTS domain_composer_settings;
