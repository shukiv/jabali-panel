-- GH #371: send-only mailboxes. A send-only account authenticates for SMTP
-- submission (Stalwart directory queryLogin) but is excluded from delivery
-- (queryRecipient gains `AND send_only = 0`), so it can send but never
-- receives or stores mail. Useful for per-appliance/per-service notification
-- credentials with isolation (compromise one, rotate one).
ALTER TABLE mailboxes
  ADD COLUMN send_only TINYINT(1) NOT NULL DEFAULT 0 AFTER is_disabled;
