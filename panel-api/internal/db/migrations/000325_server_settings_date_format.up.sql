-- GH #2164: the date format the panel shows times with, picked by the admin
-- in Server Settings -> Server Time next to the timezone. One of a fixed list
-- of dayjs patterns (paneltime.DateFormats); the default reads 16/10/26 06:36.
--
-- TEXT for the server_settings row-size ceiling (GH #1766). At the ceiling an
-- ADD COLUMN of TEXT alone can still fail with ERROR 1118, so the same ALTER
-- relieves it: timezone moves from VARCHAR(64) to off-page TEXT. timezone has
-- no index and no foreign key, and panel-api has no AutoMigrate, so the change
-- is transparent (the model tag says type:text to match).
ALTER TABLE server_settings
    MODIFY timezone TEXT NOT NULL DEFAULT (''),
    ADD COLUMN date_format TEXT NOT NULL DEFAULT ('DD/MM/YY HH:mm');
