-- GH #1449: make Web / Mail / DNS independent services per domain.
--   web_disabled: when 1 the domain has NO web vhost / docroot / PHP — a
--     docroot-less entry (DNS-only zone, or mail-only domain). Mail already
--     toggles via mail_provider; this adds the web toggle.
--   dns_disabled: when 1 the reconciler does NOT create/converge a PowerDNS
--     zone for this domain — the tenant runs DNS elsewhere (external DNS).
--
-- Stored INVERTED (disabled, DEFAULT 0) on purpose: the zero value means the
-- service is ON, so (a) the whole existing fleet stays full-service with no
-- data change, (b) the non-default state is a NON-zero value, which GORM
-- always writes — sidestepping the email_enabled zero-value scar (migration
-- 000123) without every create having to set the flag, and (c) every existing
-- in-memory test fixture keeps its web+dns behaviour untouched.
ALTER TABLE domains
  ADD COLUMN web_disabled TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN dns_disabled TINYINT(1) NOT NULL DEFAULT 0;
