-- Reverse GH #1701 Slice 2 per-domain PHP flags.
ALTER TABLE domains
  DROP COLUMN php_log_errors,
  DROP COLUMN php_file_uploads,
  DROP COLUMN php_short_open_tag;
