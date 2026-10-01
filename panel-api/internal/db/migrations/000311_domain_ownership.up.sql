-- GH #1816 / ADR-0170: domain ownership proof.
--
-- A tenant domain stays PENDING until its owner proves control of the name
-- (a TXT record read through public resolvers) or an administrator approves
-- it. While pending, the reconciler keeps the zone unpublished, the recursor
-- forward absent, mail unregistered and the real-name vhost closed to the
-- public (ADR-0170 section 2).
--
--   ownership_status             pending | verified. The DEFAULT is 'pending'
--                                so any insert that forgets the field fails
--                                closed.
--   ownership_method             how the row became verified: dns_txt, admin,
--                                parent, legacy, migration, restore,
--                                automation, policy_off ('' while pending).
--   ownership_token              the per-row challenge value (hex, 64 chars).
--                                Public once the tenant publishes it.
--   ownership_pending_since      when the row last entered pending; the
--                                14-day expiry counts from here.
--   ownership_verified_at        when the row became verified. NULL on a row
--                                that was never verified (only those expire).
--   ownership_checked_at         the last TXT check.
--   ownership_next_check_at      when the ticker checks the row next.
--   ownership_last_result        the last check's outcome, shown to the
--                                tenant (not_found, mismatch, ...).
--   ownership_expiry_notified_at when the day-10 expiry notice was sent.
--
-- web_domain_aliases carries the same state: an alias outside a verified
-- domain of the same owner needs its own proof before it joins the vhost or
-- the certificate.
--
-- The policy switch lives in its own singleton table, not server_settings:
-- server_settings is at InnoDB's in-row size ceiling (GH #1766). The
-- application treats a missing row as "proof required" (fail closed); this
-- migration only creates the table.
--
-- Existing rows are grandfathered as verified/legacy. The backfill DML runs
-- LAST, after every DDL statement, so a failure part-way leaves no half-set
-- data behind a missing column.
ALTER TABLE domains
  ADD COLUMN ownership_status             VARCHAR(16) NOT NULL DEFAULT 'pending',
  ADD COLUMN ownership_method             VARCHAR(16) NOT NULL DEFAULT '',
  ADD COLUMN ownership_token              VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN ownership_pending_since      DATETIME(6) NULL,
  ADD COLUMN ownership_verified_at        DATETIME(6) NULL,
  ADD COLUMN ownership_checked_at         DATETIME(6) NULL,
  ADD COLUMN ownership_next_check_at      DATETIME(6) NULL,
  ADD COLUMN ownership_last_result        VARCHAR(32) NOT NULL DEFAULT '',
  ADD COLUMN ownership_expiry_notified_at DATETIME(6) NULL,
  ADD INDEX ix_domains_ownership_due (ownership_status, ownership_next_check_at);

ALTER TABLE web_domain_aliases
  ADD COLUMN ownership_status             VARCHAR(16) NOT NULL DEFAULT 'pending',
  ADD COLUMN ownership_method             VARCHAR(16) NOT NULL DEFAULT '',
  ADD COLUMN ownership_token              VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN ownership_pending_since      DATETIME(6) NULL,
  ADD COLUMN ownership_verified_at        DATETIME(6) NULL,
  ADD COLUMN ownership_checked_at         DATETIME(6) NULL,
  ADD COLUMN ownership_next_check_at      DATETIME(6) NULL,
  ADD COLUMN ownership_last_result        VARCHAR(32) NOT NULL DEFAULT '',
  ADD COLUMN ownership_expiry_notified_at DATETIME(6) NULL,
  ADD INDEX ix_web_domain_aliases_ownership_due (ownership_status, ownership_next_check_at);

CREATE TABLE domain_ownership_settings (
  id            TINYINT UNSIGNED NOT NULL DEFAULT 1,
  require_proof TINYINT(1)       NOT NULL DEFAULT 1,
  updated_by    VARCHAR(64)      NOT NULL DEFAULT '',
  updated_at    DATETIME(3)      NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT domain_ownership_settings_singleton CHECK (id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- DML last: grandfather every row that exists today.
UPDATE domains
   SET ownership_status = 'verified',
       ownership_method = 'legacy',
       ownership_verified_at = UTC_TIMESTAMP(6);

UPDATE web_domain_aliases
   SET ownership_status = 'verified',
       ownership_method = 'legacy',
       ownership_verified_at = UTC_TIMESTAMP(6);
