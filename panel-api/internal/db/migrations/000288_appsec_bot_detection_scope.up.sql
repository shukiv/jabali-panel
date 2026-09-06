-- AppSec bot-detection SCOPE (CrowdSec 1.8, opt-in follow-up).
--   appsec_bot_detection_scope ∈ {"all","selected"}: "all" challenges every
--   site when bot detection is on (carve exceptions via domains.bot_challenge_
--   exempt); "selected" challenges ONLY domains flagged bot_challenge_include.
-- server_settings string column: TEXT + parenthesized default (row-size
-- ceiling — migrations 000264/000285/000286). Default 'all' = shipped behaviour.
ALTER TABLE server_settings
  ADD COLUMN appsec_bot_detection_scope TEXT NOT NULL DEFAULT ('all');

-- Per-domain opt-IN (used only when the scope is "selected"). Admin-only.
ALTER TABLE domains
  ADD COLUMN bot_challenge_include TINYINT(1) NOT NULL DEFAULT 0;
