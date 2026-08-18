-- GH #1161: opt-in exposure of the Automation API on :443 (in addition to the
-- default :8443). Billing systems on hosts with a locked-down outbound firewall
-- (CSF's default TCP_OUT allows ~20/21/22/25/53/80/110/113/443 and NOT 8443)
-- cannot reach :8443 at all, and the failure ("Connection refused") looks like
-- the panel being down rather than a blocked port. This flag lets an admin also
-- serve the API on the standard 443 port.
--
-- Default 0 (OFF): serving the API on the public 443 port is new exposure, so it
-- is opt-in. When enabled, the reconciler has the agent write the nginx include
-- that adds the `location ^~ /api/v1/automation/` proxy to the panel-hostname
-- :443 vhost; disabling writes it back to empty. ONLY /api/v1/automation/ is
-- exposed (every route there is behind RequireAutomationHMAC); the internal,
-- unauthenticated endpoints (/api/v1/internal/, the malware event) stay
-- :8443-only, guarded by the 8443 vhost's 404 blocks which :443 never gains.
ALTER TABLE server_settings
    ADD COLUMN automation_api_public_enabled TINYINT(1) NOT NULL DEFAULT 0;
