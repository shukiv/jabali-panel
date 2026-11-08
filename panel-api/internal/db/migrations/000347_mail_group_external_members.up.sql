-- GH #1833: a distribution list can deliver to addresses outside this server
-- (a Gmail address, say) next to its mailboxes. Since #1845 such a list is a
-- mailing list on the mail server, which delivers to any address. A Shared
-- Workspace group stays mailbox-only: its members share one mailbox.
--
-- address is lower-cased and validated by the panel; an address on a domain
-- hosted on this server is a mailbox member instead.
CREATE TABLE mail_group_external_members (
  group_id   CHAR(26)     NOT NULL,
  address    VARCHAR(320) NOT NULL,
  created_at DATETIME(6)  NOT NULL,
  PRIMARY KEY (group_id, address),
  CONSTRAINT fk_mail_group_external_members_group
    FOREIGN KEY (group_id) REFERENCES mail_groups(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The most external addresses one list may hold, set by the admin, so a list
-- cannot be turned into a bulk mailer. 0 allows none. SMALLINT keeps the
-- server_settings row small (row-size ceiling, GH #1766).
ALTER TABLE server_settings
  ADD COLUMN mail_list_external_member_cap SMALLINT UNSIGNED NOT NULL DEFAULT 25;
