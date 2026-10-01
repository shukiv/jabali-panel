-- Restores the column with its definition from migration 000203. The old
-- values are not restored: every user gets the default 1 (webmail on), which
-- nothing reads.
ALTER TABLE users ADD COLUMN webmail_enabled TINYINT(1) NOT NULL DEFAULT 1;
