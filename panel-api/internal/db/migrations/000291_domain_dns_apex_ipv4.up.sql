-- GH #1540: "Add DNS Zone" — a DNS-only zone (web_disabled=1) can be created
-- with a tenant-chosen apex IP (the "pointed IP" johnnyq asked for). A web-off
-- zone's apex A is deliberately NOT seeded by BootstrapRecords (includeApex=
-- false) and NOT re-asserted by convergeApexAddrRecords (web-gated), so there
-- is no managed apex row to carry the address — persist it on the domain row so
-- the reconciler can seed the single apex A once at zone bootstrap.
--
-- Nullable, no default: only DNS-only creates set it; every existing row and
-- every web/mail create leaves it NULL (the apex is panel-managed or absent).
-- Read once at zone bootstrap and never written again, so no zero-value scar.
ALTER TABLE domains
  ADD COLUMN dns_apex_ipv4 VARCHAR(45) NULL;
