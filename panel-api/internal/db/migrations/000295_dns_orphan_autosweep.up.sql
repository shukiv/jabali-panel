-- GH #1620: opt-in automatic PowerDNS orphan-record sweep.
--
-- server_settings.dns_orphan_autosweep_enabled — global on/off toggle,
-- OFF by default. When on, the reconciler fires the agent RPC
-- `dns.reap-orphans` with apply=true at most once per hour, so every box
-- self-removes PowerDNS backend rows whose parent domain is gone
-- (records/domainmetadata/comments/cryptokeys with
-- domain_id NOT IN (SELECT id FROM domains)). The agent refuses the
-- sweep outright when its domains table is empty, so an enabled-but-
-- misconfigured box deletes nothing. Manual operators keep the existing
-- one-shot CLI (`jabali dns prune-orphan-records`) regardless of this
-- toggle.

ALTER TABLE server_settings
    ADD COLUMN dns_orphan_autosweep_enabled TINYINT(1) NOT NULL DEFAULT 0;
