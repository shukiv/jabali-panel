-- GH #1332 (item 13): per-user Composer version channel for the shell `composer`.
-- NULL = latest (default); "lts" = the 2.2 LTS. Applied by the host dispatcher.
ALTER TABLE users
  ADD COLUMN composer_channel VARCHAR(16) NULL DEFAULT NULL;
