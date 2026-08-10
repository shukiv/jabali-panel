-- JAB-235: DNS-01 fallback for CDN-fronted domains.
--
-- cf_api_token_enc holds the operator's Cloudflare API token sealed with
-- ssokey (AES-256-GCM, nonce||ciphertext||tag — same convention as
-- automation_tokens.secret_enc). Written ONLY by the dedicated admin
-- endpoint; never returned by any API response. NULL = not configured.
--
-- ssl_certificates.issue_method records which ACME challenge issued the
-- current cert ('' = unknown/legacy, 'http-01', 'dns-01') so the SSL UI and
-- the operator can tell a DNS-01 lineage from a webroot one.
ALTER TABLE server_settings
  ADD COLUMN cf_api_token_enc varbinary(512) NULL;

ALTER TABLE ssl_certificates
  ADD COLUMN issue_method varchar(16) NOT NULL DEFAULT '';
