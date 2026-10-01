DROP TABLE IF EXISTS domain_ownership_settings;

ALTER TABLE web_domain_aliases
  DROP INDEX ix_web_domain_aliases_ownership_due,
  DROP COLUMN ownership_status,
  DROP COLUMN ownership_method,
  DROP COLUMN ownership_token,
  DROP COLUMN ownership_pending_since,
  DROP COLUMN ownership_verified_at,
  DROP COLUMN ownership_checked_at,
  DROP COLUMN ownership_next_check_at,
  DROP COLUMN ownership_last_result,
  DROP COLUMN ownership_expiry_notified_at;

ALTER TABLE domains
  DROP INDEX ix_domains_ownership_due,
  DROP COLUMN ownership_status,
  DROP COLUMN ownership_method,
  DROP COLUMN ownership_token,
  DROP COLUMN ownership_pending_since,
  DROP COLUMN ownership_verified_at,
  DROP COLUMN ownership_checked_at,
  DROP COLUMN ownership_next_check_at,
  DROP COLUMN ownership_last_result,
  DROP COLUMN ownership_expiry_notified_at;
