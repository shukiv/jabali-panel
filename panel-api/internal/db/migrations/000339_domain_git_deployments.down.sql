ALTER TABLE domain_git_repos
  DROP COLUMN deploy_commands,
  DROP COLUMN shared_paths,
  DROP COLUMN web_dir;
DROP TABLE IF EXISTS domain_git_deployments;
