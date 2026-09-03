-- Per-domain opt-out from the server-wide AppSec bot-detection challenge
-- (CrowdSec 1.8). Admin-only flag: when set, the domain (and www.<domain>) is
-- exempted from the JS/proof-of-work challenge even while server-wide bot
-- detection is on — for API/webhook-heavy sites. Default 0 (not exempt).
ALTER TABLE domains
  ADD COLUMN bot_challenge_exempt TINYINT(1) NOT NULL DEFAULT 0;
