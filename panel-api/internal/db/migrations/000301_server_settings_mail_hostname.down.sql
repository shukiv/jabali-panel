-- Reverse JAB-390 Step 1: drop the optional mail-hostname override.
-- Safe unconditionally — the column has no writer in this slice, so it
-- only ever holds NULL; nothing downstream reads a value from it.
--
-- The captcha columns are intentionally left as TEXT (the up migration
-- widened them from VARCHAR(512) to relieve the row-size ceiling, GH #1766).
-- Reverting them to VARCHAR would push in-row bytes back over InnoDB's
-- 65535-byte limit and re-trigger ERROR 1118, so the down migration keeps
-- the strictly-safer off-page TEXT form — string values round-trip
-- identically either way.
ALTER TABLE server_settings
  DROP COLUMN mail_hostname;
