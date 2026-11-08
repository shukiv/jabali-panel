-- Reverse GH #1833: distribution lists hold mailboxes only again.
ALTER TABLE server_settings
  DROP COLUMN mail_list_external_member_cap;

DROP TABLE mail_group_external_members;
