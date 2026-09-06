-- CrowdSec 1.8 AppSec bot detection (per-server, default OFF).
-- Mode ∈ {"off","balanced","permissive"}: off = no challenge;
-- balanced rejects at fingerprint score >= 75; permissive >= 100.
-- The agent (security.crowdsec.appsec.botdetection.set) composes the
-- upstream appsec-bot-challenge configs into the AppSec acquisition and
-- serves a self-contained JS/proof-of-work challenge via the nginx bouncer.
--
-- TEXT (not VARCHAR) so it lives off-page: the server_settings row already
-- sits near InnoDB's 65535-byte in-row ceiling (see migrations 000264 /
-- 000285). A parenthesized default is required for TEXT/BLOB in MariaDB.
ALTER TABLE server_settings
  ADD COLUMN appsec_bot_detection TEXT NOT NULL DEFAULT ('off');
