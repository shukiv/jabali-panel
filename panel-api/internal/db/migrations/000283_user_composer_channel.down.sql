-- Reverse GH #1332 per-user Composer channel.
ALTER TABLE users
  DROP COLUMN composer_channel;
