-- GH #1833: a distribution list with outside addresses now delivers by Sieve
-- redirect from the list address, one redirect per recipient, and the mail
-- server allows 20 per message. An outside address past 20 would silently get
-- nothing, so the admin's cap on them stops at 20 and defaults to it. A box
-- still on the old default of 25, or set higher, moves to 20; a lower value an
-- admin chose is kept.
ALTER TABLE server_settings ALTER COLUMN mail_list_external_member_cap SET DEFAULT 20;
UPDATE server_settings SET mail_list_external_member_cap = 20 WHERE mail_list_external_member_cap > 20;
