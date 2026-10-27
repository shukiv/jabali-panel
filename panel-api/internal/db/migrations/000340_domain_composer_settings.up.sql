-- GH #2276, ADR-0176: the Composer project folder a domain uses, and the
-- folder each run ran in.
--
-- domain_composer_settings has one row per domain that names its own project
-- folder; a domain with no row finds composer.json from its document root
-- up to domains/<domain>/, as before. project_dir is relative to the user's
-- home and is "domains" or starts with "domains/": several domains of one
-- user can share one project, e.g. a composer.json and vendor/ in
-- ~/domains/.
--
-- composer_runs.work_dir is the folder a run ran in, in the same form. One
-- run at a time per domain and per folder: two domains sharing a project
-- can't run Composer in it at once. Existing runs get theirs from their
-- domain's name and app_dir.
CREATE TABLE domain_composer_settings (
  domain_id   CHAR(26)     NOT NULL,
  project_dir VARCHAR(255) NOT NULL,
  created_at  DATETIME(6)  NOT NULL,
  updated_at  DATETIME(6)  NOT NULL,
  PRIMARY KEY (domain_id),
  CONSTRAINT fk_domain_composer_settings_domain FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE composer_runs
  ADD COLUMN work_dir VARCHAR(255) NOT NULL DEFAULT '' AFTER app_dir,
  ADD KEY idx_composer_runs_user_state (user_id, state);

UPDATE composer_runs r JOIN domains d ON d.id = r.domain_id
  SET r.work_dir = IF(r.app_dir = '', CONCAT('domains/', d.name), CONCAT('domains/', d.name, '/', r.app_dir));
