-- GH #2032: a tenant's own Cloudflare API token for ACME DNS-01. For each
-- certificate name, the panel writes the _acme-challenge record with the
-- token of the user who owns the name's domain, and falls back to the
-- server's token (server_settings.cf_api_token_enc, JAB-235) only when that
-- token cannot reach the zone. One tenant's token is never used for another
-- tenant's names.
--
-- token_enc is the token sealed with the sso key (AES-256-GCM), the same
-- envelope as cf_api_token_enc; the plaintext is never stored or returned.
-- verified_at is when "Verify & Save" last proved the token can write DNS.
-- Schema only; no seed rows.
CREATE TABLE user_cloudflare_tokens (
  user_id     CHAR(26)       NOT NULL PRIMARY KEY,
  token_enc   VARBINARY(512) NOT NULL,
  verified_at DATETIME(6)    NOT NULL,
  updated_at  DATETIME(6)    NOT NULL,
  CONSTRAINT fk_uct_user
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
