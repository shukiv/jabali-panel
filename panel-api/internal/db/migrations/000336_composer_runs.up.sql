-- GH #2276, ADR-0176: Composer runs a user starts from the panel for one of
-- their domains, and the hosting-package entitlement that allows them.
--
-- A run is a transient unit on the server (jabali-composer-<id>.service).
-- The row records what was asked, and the result once the panel has
-- collected it: state, exit code and the last 256 KiB of the log.
-- state: running | succeeded | failed | timed_out | cancelled.
--
-- composer_enabled defaults to 0 for every package, existing ones included:
-- a run executes the user's composer.json scripts with the exec functions
-- allowed, so an admin grants it.
CREATE TABLE composer_runs (
  id                  CHAR(26)     NOT NULL,
  user_id             CHAR(26)     NOT NULL,
  domain_id           CHAR(26)     NOT NULL,
  app_dir             VARCHAR(255) NOT NULL DEFAULT '',
  command             VARCHAR(32)  NOT NULL,
  packages            TEXT         NULL,
  no_dev              TINYINT(1)   NOT NULL DEFAULT 0,
  php_version         VARCHAR(8)   NOT NULL DEFAULT '',
  state               VARCHAR(16)  NOT NULL,
  exit_code           INT          NULL,
  result              VARCHAR(32)  NOT NULL DEFAULT '',
  php_defense_blocked TINYINT(1)   NOT NULL DEFAULT 0,
  log                 MEDIUMTEXT   NULL,
  log_truncated       TINYINT(1)   NOT NULL DEFAULT 0,
  error               VARCHAR(1024) NOT NULL DEFAULT '',
  started_at          DATETIME(6)  NOT NULL,
  finished_at         DATETIME(6)  NULL,
  created_at          DATETIME(6)  NOT NULL,
  updated_at          DATETIME(6)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_composer_runs_domain_created (domain_id, created_at),
  KEY idx_composer_runs_state (state),
  CONSTRAINT fk_composer_runs_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_composer_runs_domain FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE hosting_packages
  ADD COLUMN composer_enabled TINYINT(1) NOT NULL DEFAULT 0;
