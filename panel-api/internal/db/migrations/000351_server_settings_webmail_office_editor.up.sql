-- GH #2501: webmail's document editor. Bulwark opens office files from its
-- Files view in any WOPI editor (EuroOffice, ONLYOFFICE, Collabora) when
-- WOPI_CLIENT_URL names one. The agent writes the URL into webmail's own
-- environment file (bulwark-editor.env); empty = editing off. One value for
-- the whole server, set in Server Settings -> Email.
ALTER TABLE server_settings
  ADD COLUMN webmail_office_editor_url VARCHAR(255) NOT NULL DEFAULT '';