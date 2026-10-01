-- GH #1701 Slice 2: three more per-domain PHP directives, rendered through the
-- same fastcgi_param PHP_VALUE as the other per-domain settings.
--   php_log_errors     — log_errors
--   php_file_uploads   — file_uploads
--   php_short_open_tag — short_open_tag
-- All NULL = inherit (the pool's flag, else the box php.ini). The agent pins all
-- three on every PHP vhost, so a value set on one domain cannot stick on a
-- reused FPM worker and bleed onto a sibling domain on the same pool.
ALTER TABLE domains
  ADD COLUMN php_log_errors TINYINT(1) NULL DEFAULT NULL,
  ADD COLUMN php_file_uploads TINYINT(1) NULL DEFAULT NULL,
  ADD COLUMN php_short_open_tag TINYINT(1) NULL DEFAULT NULL;
