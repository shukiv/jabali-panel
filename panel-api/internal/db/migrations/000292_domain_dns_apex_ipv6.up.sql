-- GH #1540 follow-up: a DNS-only zone (web_disabled=1) may also carry an apex
-- IPv6 (the AAAA record), alongside the IPv4 apex added in 000291. Same shape:
-- read once by the reconciler at zone bootstrap to seed the "@ AAAA <ip>" row,
-- then never written again. Nullable, no default — only DNS-only creates that
-- pass an IPv6 set it; every other row stays NULL.
ALTER TABLE domains
  ADD COLUMN dns_apex_ipv6 VARCHAR(45) NULL;
