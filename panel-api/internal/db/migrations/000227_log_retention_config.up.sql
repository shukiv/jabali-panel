-- Operator-configurable log/report retention (Server Settings -> Logs). One JSON
-- column on the single-row server_settings holds a { category: days } map; an
-- absent category falls back to the code default (90d baseline, 365d security,
-- 0 = keep-forever for the hash-chained audit log). Closes the "make retention
-- configurable" ask + backs JAB-102/105/124.
ALTER TABLE server_settings
  ADD COLUMN log_retention JSON NULL;
