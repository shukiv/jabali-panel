-- JAB-171 phase 4b: admin-configurable allowlist of channel kinds a tenant may
-- create. JSON array in a longtext column. Empty/NULL is interpreted by the app
-- as the safe default set (ntfy/telegram/discord/webpush) — webhook/slack/sms
-- are admin opt-in. No seed: the app substitutes the default when the column is
-- empty, so a fresh row needs no data migration.
ALTER TABLE server_settings
  ADD COLUMN tenant_notification_kinds LONGTEXT NULL;
