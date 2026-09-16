-- Reverse JAB-390 Step 1: drop the optional mail-hostname override.
-- Safe unconditionally — the column has no writer in this slice, so it
-- only ever holds NULL; nothing downstream reads a value from it.
ALTER TABLE server_settings
  DROP COLUMN mail_hostname;
