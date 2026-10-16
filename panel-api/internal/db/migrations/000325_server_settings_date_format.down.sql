-- Reverse GH #2164: drop the date format.
--
-- timezone stays TEXT. The up migration moved it off-page to relieve the
-- row-size ceiling (GH #1766); turning it back into VARCHAR(64) could push the
-- row over InnoDB's limit again and fail with ERROR 1118. Values round-trip
-- the same either way.
ALTER TABLE server_settings
    DROP COLUMN date_format;
