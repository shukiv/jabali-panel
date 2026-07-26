-- GH #648 (DMARCbis): per-domain settable np (non-existent subdomain policy)
-- and t=y (testing) tags, folded into the canonical _dmarc record by the
-- reconciler. Empty np = omit (receiver falls back to sp). Schema-only.
ALTER TABLE domains
  ADD COLUMN dmarc_np VARCHAR(12) NOT NULL DEFAULT '',
  ADD COLUMN dmarc_testing TINYINT(1) NOT NULL DEFAULT 0;
