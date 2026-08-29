-- GH #1332 (item 14): per-domain environment variables, delivered to PHP as
-- nginx fastcgi_param (reach getenv() + $_SERVER). Stored as a JSON array of
-- {key,value}. Keys are validated against a security denylist in the API/agent
-- (a fastcgi_param named PHP_ADMIN_VALUE would override the FPM sandbox).
ALTER TABLE domains
  ADD COLUMN env_vars JSON NULL DEFAULT NULL;
