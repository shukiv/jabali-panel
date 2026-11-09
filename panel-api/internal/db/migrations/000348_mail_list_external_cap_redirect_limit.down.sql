-- A cap lowered to 20 stays 20: the old value is not recorded.
ALTER TABLE server_settings ALTER COLUMN mail_list_external_member_cap SET DEFAULT 25;
