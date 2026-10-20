-- The extra IP blocklists (internal/blocklists catalog keys, comma
-- separated) the admin added to CrowdSec on top of its community list.
-- Empty = none, the default. The reconciler sends the set to the agent,
-- which imports each list as ban decisions and removes the lists not set.
ALTER TABLE server_settings ADD COLUMN crowdsec_blocklists TEXT NOT NULL DEFAULT '';
