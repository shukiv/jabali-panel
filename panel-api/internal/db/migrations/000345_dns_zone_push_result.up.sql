-- GH #2377: record what PowerDNS said about the last push of each zone, so the
-- DNS Zones page can tell "on the DNS server" apart from "only in the panel
-- database". last_pushed_at = when the server last accepted the zone;
-- last_push_error = why the most recent push failed (empty once one succeeds).
-- Additive and nullable: NULL/empty on every existing row until the reconciler
-- pushes the zone again.
ALTER TABLE dns_zones
  ADD COLUMN last_pushed_at DATETIME(6) NULL,
  ADD COLUMN last_push_error TEXT NULL;
