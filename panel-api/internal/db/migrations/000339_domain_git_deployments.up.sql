-- GH #2277, ADR-0178: Git deployments of a domain, and what a deployment
-- does besides fetching the repository.
--
-- A deployment is a transient unit on the server
-- (jabali-gitdeploy-<id>.service) that runs as the domain's owner, in their
-- SSH sandbox. It fetches the branch into domains/<domain>/releases/<id>,
-- links the shared paths, runs composer install and the deploy commands, and
-- then switches domains/<domain>/current to the new release. A rollback
-- (kind rollback) only switches current back to an earlier release.
--
-- release_id is the release the run makes live: its own id for a
-- deployment, the earlier deployment's id for a rollback. doc_root is the
-- document root the domain gets once the run has succeeded:
-- /home/<user>/domains/<domain>/current[/<web_dir>]. os_user is the system
-- account the run ran as: its credential file is in that account's home
-- until the panel releases the run.
-- state: running | succeeded | failed | timed_out | cancelled.
CREATE TABLE domain_git_deployments (
  id                  CHAR(26)      NOT NULL,
  user_id             CHAR(26)      NOT NULL,
  domain_id           CHAR(26)      NOT NULL,
  os_user             VARCHAR(32)   NOT NULL,
  kind                VARCHAR(16)   NOT NULL,
  release_id          CHAR(26)      NOT NULL,
  branch              VARCHAR(255)  NOT NULL DEFAULT '',
  commit_sha          VARCHAR(64)   NOT NULL DEFAULT '',
  doc_root            VARCHAR(512)  NOT NULL,
  php_version         VARCHAR(8)    NOT NULL DEFAULT '',
  state               VARCHAR(16)   NOT NULL,
  exit_code           INT           NULL,
  result              VARCHAR(32)   NOT NULL DEFAULT '',
  php_defense_blocked TINYINT(1)    NOT NULL DEFAULT 0,
  log                 MEDIUMTEXT    NULL,
  log_truncated       TINYINT(1)    NOT NULL DEFAULT 0,
  error               VARCHAR(1024) NOT NULL DEFAULT '',
  started_at          DATETIME(6)   NOT NULL,
  finished_at         DATETIME(6)   NULL,
  created_at          DATETIME(6)   NOT NULL,
  updated_at          DATETIME(6)   NOT NULL,
  PRIMARY KEY (id),
  KEY idx_domain_git_deployments_domain_created (domain_id, created_at),
  KEY idx_domain_git_deployments_state (state),
  CONSTRAINT fk_domain_git_deployments_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_domain_git_deployments_domain FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- web_dir is the directory the site serves, relative to the repository (""
-- is its root). shared_paths holds the paths kept across releases in
-- domains/<domain>/shared, one per line. deploy_commands holds the commands a
-- deployment runs in the new release before it goes live, one per line.
ALTER TABLE domain_git_repos
  ADD COLUMN web_dir         VARCHAR(255) NOT NULL DEFAULT '',
  ADD COLUMN shared_paths    TEXT         NULL,
  ADD COLUMN deploy_commands TEXT         NULL;
