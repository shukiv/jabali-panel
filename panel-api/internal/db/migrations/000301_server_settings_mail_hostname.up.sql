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
-- TEXT, not VARCHAR: the server_settings row already carries many
-- VARCHAR columns and is close to the MariaDB 65535-byte row-size
-- ceiling, where another in-row VARCHAR risks "ERROR 1118: Row size too
-- large". TEXT is stored off-row (only a small pointer in-row). See the
-- feedback_server_settings_row_ceiling note. NULL default, no backfill.
ALTER TABLE server_settings
  ADD COLUMN mail_hostname TEXT NULL;
