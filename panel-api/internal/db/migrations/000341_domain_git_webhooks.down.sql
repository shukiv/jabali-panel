ALTER TABLE domain_git_deployments
  DROP COLUMN source;
DROP TABLE IF EXISTS domain_git_webhooks;
