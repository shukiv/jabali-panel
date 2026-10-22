-- Security advisories apply themselves by default.
--
-- jabali-security-feed.timer checks Jabali's security advisories hourly
-- (panel-api/cmd/server/security_feed.go). security_enabled decides whether a
-- server applies an advisory's fix right away (installs the vendor update,
-- runs the panel update) or only alerts the admins. It is separate from
-- jabali_enabled: panel self-update stays opt-in, while a fix for a security
-- flaw installs itself the way it does on other hosting panels.
--
-- The column default stays FALSE for the same reason as apt_enabled
-- (models.UpdateAutoupdateConfig): GORM leaves a zero bool out of an INSERT
-- when the field has a default, so a TRUE column default would turn every
-- explicit disable back on. Fresh installs seed TRUE in EnsureDefault; this
-- migration turns it on for existing servers, which have never had the
-- setting, so no admin has chosen it off.
--
-- Statement order: DDL first, the DML last (see 000272): golang-migrate runs
-- the file as one multi-statement Exec, and DDL after DML fails the batch.

ALTER TABLE update_autoupdate_config
  ADD COLUMN security_enabled BOOLEAN NOT NULL DEFAULT FALSE AFTER jabali_time;

UPDATE update_autoupdate_config SET security_enabled = TRUE;
