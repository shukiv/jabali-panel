-- DNS-01 wildcard issuance for shared certificates (preview URLs + tenant
-- wildcards). `source` records how the cert material came to exist:
--   'uploaded' — operator pasted cert+key (JAB-170 original flow)
--   'acme'     — panel issued it via certbot DNS-01; renewals are driven
--                by certbot (hooks persisted in the renewal conf) with a
--                daily panel sweep syncing row expiry from disk.
-- last_error surfaces the most recent failed ACME attempt; staging marks
-- Let's Encrypt staging-CA certs so the UI can badge them as untrusted.
ALTER TABLE shared_certificates
  ADD COLUMN source varchar(16) NOT NULL DEFAULT 'uploaded',
  ADD COLUMN last_error text NULL,
  ADD COLUMN staging tinyint(1) NOT NULL DEFAULT 0,
  ADD COLUMN last_attempt_at datetime(6) NULL;
