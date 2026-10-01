-- Reverse GH #1701 Slice 3 per-domain PHP admin values.
ALTER TABLE domains
  DROP COLUMN php_open_basedir,
  DROP COLUMN php_allow_url_fopen;
