-- When each mailbox's password last changed (NULL: it still has the
-- password it was created with). The mail server keeps the app passwords a
-- mailbox creates (webmail > Security) apart from the panel's mailbox
-- password, so the panel removes every app password made on a mailbox up to
-- a minute after its last password change, and every app password and API
-- key on a mailbox that may not sign in (the reconciler's mail credentials
-- pass).
--
-- Existing mailboxes get the time of this migration: app passwords created
-- before the update are removed once, on the first passes after it.
--
-- The UPDATE is last, after the schema change (DML last).
ALTER TABLE mailboxes
  ADD COLUMN password_changed_at DATETIME(6) NULL;

UPDATE mailboxes SET password_changed_at = UTC_TIMESTAMP(6);
