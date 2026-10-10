-- GH #2017: the mail server's spam score thresholds belong to the panel.
-- install.sh used to re-apply 5 / 15 / 20 to Stalwart's SpamSettings on every
-- install and update, so an admin's change never lasted. The reconciler now
-- converges these columns into SpamSettings, and install.sh no longer sets
-- the scores.
--
-- A message scoring at or above spam_junk_score goes to the Junk folder; at
-- or above spam_reject_score it is refused at SMTP time; at or above
-- spam_discard_score it is dropped. 0 turns reject or discard off.
-- The defaults are the values install.sh applied, so an update changes
-- nothing until the admin does.
ALTER TABLE server_settings
  ADD COLUMN spam_junk_score DOUBLE NOT NULL DEFAULT 5,
  ADD COLUMN spam_reject_score DOUBLE NOT NULL DEFAULT 15,
  ADD COLUMN spam_discard_score DOUBLE NOT NULL DEFAULT 20;
