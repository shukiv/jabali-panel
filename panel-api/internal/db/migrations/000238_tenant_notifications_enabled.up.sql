-- JAB-171 phase 3b: master switch for the tenant-facing notification surface
-- (/me/notifications/*). Default OFF and there is deliberately NO admin toggle
-- in phase 3b — the endpoints exist, are ownership-scoped, redact secrets and
-- are OpenAPI-documented, but stay physically un-enablable until phase 4 lands
-- the SSRF guard, per-kind allowlist and email-verify. Only phase 4 adds the
-- admin control that can flip this on. Follows the TenantDomainOptionsEnabled
-- default-off tenant-cap precedent (security-sensitive caps default OFF).
ALTER TABLE server_settings
  ADD COLUMN tenant_notifications_enabled TINYINT(1) NOT NULL DEFAULT 0;
