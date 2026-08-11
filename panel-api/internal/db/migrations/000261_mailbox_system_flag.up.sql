-- GH #1056: mark panel-managed infrastructure principals so they can be
-- filtered out of the user-facing mailbox lists. The only such principal
-- today is the JAB-230 sendmail relay (noreply@<domain> / jabali-noreply@),
-- a SendOnly account created by the reconciler with the display-name suffix
-- " (system sender)". It stays fully functional (Stalwart, backups, disk
-- accounting still see it) — it just no longer surfaces as a mailbox the
-- operator never created.
ALTER TABLE mailboxes
  ADD COLUMN system tinyint(1) NOT NULL DEFAULT 0;

-- Retrofit existing relay rows on already-provisioned boxes. Scoped tightly
-- to the reconciler's own signature (SendOnly + the exact display-name suffix
-- it writes) so a human's send-only mailbox is never mislabelled as system.
UPDATE mailboxes
  SET system = 1
  WHERE send_only = 1
    AND display_name LIKE '% (system sender)';
