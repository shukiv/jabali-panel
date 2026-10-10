-- GH #2056: see 000316_website_mail_smarthost.up.sql.
ALTER TABLE server_settings
  DROP COLUMN website_mail_mode,
  DROP COLUMN smarthost_host,
  DROP COLUMN smarthost_port,
  DROP COLUMN smarthost_tls,
  DROP COLUMN smarthost_username,
  DROP COLUMN smarthost_password_enc;
