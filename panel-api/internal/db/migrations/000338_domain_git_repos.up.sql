-- GH #2277, ADR-0178: the Git repository a domain deploys from, and the
-- hosting-package entitlement that allows it.
--
-- One row per domain. token_enc is the access token sealed with the panel's
-- sso key; no API response, log line or backup carries it. A token is bound
-- to the repository's host: changing the host needs the token again.
--
-- git_deploy_enabled defaults to 0 for every package, existing ones
-- included: a deployment runs the user's own commands in their SSH sandbox,
-- so an admin grants it.
CREATE TABLE domain_git_repos (
  domain_id   CHAR(26)       NOT NULL,
  user_id     CHAR(26)       NOT NULL,
  repo_url    VARCHAR(512)   NOT NULL,
  branch      VARCHAR(255)   NOT NULL,
  username    VARCHAR(128)   NOT NULL DEFAULT '',
  token_enc   VARBINARY(1024) NULL,
  verified_at DATETIME(6)    NOT NULL,
  created_at  DATETIME(6)    NOT NULL,
  updated_at  DATETIME(6)    NOT NULL,
  PRIMARY KEY (domain_id),
  KEY idx_domain_git_repos_user (user_id),
  CONSTRAINT fk_domain_git_repos_domain FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE,
  CONSTRAINT fk_domain_git_repos_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE hosting_packages
  ADD COLUMN git_deploy_enabled TINYINT(1) NOT NULL DEFAULT 0;
