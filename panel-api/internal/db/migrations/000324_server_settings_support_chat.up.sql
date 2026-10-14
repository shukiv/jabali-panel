-- The "Ask Jabali" AI support chat (Chatwoot widget from support.jabali-panel.com),
-- opted into separately for the admin panel and the client panels; both off by
-- default. support_chat_notice_dismissed hides the admin dashboard banner that
-- announces it, for every admin, once any admin closes it.

ALTER TABLE server_settings
    ADD COLUMN support_chat_admin TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN support_chat_clients TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN support_chat_notice_dismissed TINYINT(1) NOT NULL DEFAULT 0;
