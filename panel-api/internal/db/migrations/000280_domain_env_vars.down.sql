-- Reverse GH #1332 per-domain environment variables.
ALTER TABLE domains
  DROP COLUMN env_vars;
