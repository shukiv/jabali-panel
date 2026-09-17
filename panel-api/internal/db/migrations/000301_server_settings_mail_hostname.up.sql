-- JAB-390 Step 1 (foundation): optional mail-hostname override on
-- server_settings.
--
-- NULL (the default on upgrade) means "derive mail.<panel-hostname>" —
-- today's behavior via models.PanelMailHostname, unchanged. A later slice
-- adds the setter + the safe-switchover lifecycle (routability preflight,
-- keep the old host until the new one is routable + TLS'd, ACME retry,
-- rollback, convergence across cert / Stalwart identity / sendmail creds)
-- that lets an operator pin a custom mail hostname (e.g. mail.example.com)
-- independent of the panel access hostname. Until that lands the column
-- has no writer, so it stays NULL and behavior is identical.
--
-- GH #1766: the ADD COLUMN below cannot stand alone. server_settings already
-- carries ~65 VARCHAR columns and its DEFINED in-row size is at InnoDB's
-- 65535-byte ceiling, so at v300 `ADD COLUMN mail_hostname TEXT` fails with
-- "ERROR 1118: Row size too large" on any innodb_strict_mode host (the whole
-- fleet) — a genuinely broken migration, not a benign interrupt. Note the
-- 65535 check counts the in-row VARCHARs even for an off-row TEXT column, so
-- adding TEXT is NOT free: the ceiling has to be RELIEVED first by moving an
-- existing wide in-row VARCHAR off-page. Converting the two 512-byte captcha
-- columns to TEXT frees enough in-row space for the ADD to succeed (verified
-- on a real v300 schema: 141 cols, strict mode — two conversions clear 1118).
-- Neither captcha column is indexed and neither has a foreign key, so the
-- VARCHAR->TEXT change is transparent (panel-api has no GORM AutoMigrate; the
-- model struct tags are updated to type:text to match). See the
-- feedback_server_settings_row_ceiling note. NULL default, no backfill.
ALTER TABLE server_settings
  MODIFY crowdsec_captcha_site_key   TEXT NOT NULL DEFAULT '',
  MODIFY crowdsec_captcha_secret_key TEXT NOT NULL DEFAULT '',
  ADD COLUMN mail_hostname TEXT NULL;
