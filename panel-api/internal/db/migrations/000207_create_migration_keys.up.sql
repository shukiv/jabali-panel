-- GH #648: one-time migration keys for the WordPress plugin push migration.
-- Additive (new table). Only hashes of the key + upload-session token are
-- stored; plaintext key is shown to the operator once and never persisted.
CREATE TABLE migration_keys (
  id                  CHAR(26)      NOT NULL,
  job_id              CHAR(26)      NOT NULL,
  key_hash            CHAR(64)      NOT NULL,
  dest_user_id        CHAR(26)      NOT NULL,
  dest_domain_id      CHAR(26)      NOT NULL,
  dest_docroot        VARCHAR(1024) NOT NULL,
  domain_change       TINYINT(1)    NOT NULL DEFAULT 0,
  status              VARCHAR(16)   NOT NULL DEFAULT 'unclaimed',
  expires_at          DATETIME(6)   NOT NULL,
  claimed_source_url  VARCHAR(255)  NULL,
  upload_session_hash CHAR(64)      NULL,
  created_at          DATETIME(6)   NOT NULL,
  updated_at          DATETIME(6)   NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_migration_key_hash (key_hash),
  KEY idx_migration_keys_job (job_id),
  KEY idx_migration_keys_status (status)
);
