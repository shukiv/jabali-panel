ALTER TABLE domains
  DROP COLUMN bot_challenge_include;
ALTER TABLE server_settings
  DROP COLUMN appsec_bot_detection_scope;
