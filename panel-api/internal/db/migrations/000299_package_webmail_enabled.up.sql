-- GH #1628: webmail becomes a Hosting Package entitlement.
-- Slice 1 (spine): add the column only. It is stored and editable but does
-- not yet gate anything — the reconciler still ANDs the per-user
-- users.webmail_enabled flag (that rewire + the backfill of existing
-- per-user "off" accounts lands in slice 2). Default 1 (ON) so every
-- existing package, and any package created before the field is wired,
-- keeps webmail available — matching users.webmail_enabled (migration
-- 000203) and the issue's "enabled by default for all hosting packages".
ALTER TABLE hosting_packages
  ADD COLUMN webmail_enabled TINYINT(1) NOT NULL DEFAULT 1;
