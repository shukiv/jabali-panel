-- GH #860: opt-in branded page for domains that resolve to this server
-- but have no vhost. Default 0 keeps the shipped `return 444` drop, so
-- unknown-Host scanners still get nothing to fingerprint.
ALTER TABLE server_settings
  ADD COLUMN unconfigured_page_enabled tinyint(1) NOT NULL DEFAULT 0;
