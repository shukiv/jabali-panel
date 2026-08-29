-- Reverse GH #1332 per-domain PHP runtime directives.
ALTER TABLE domains
  DROP COLUMN php_display_errors,
  DROP COLUMN php_error_reporting,
  DROP COLUMN php_timezone;
