-- GH #1184: the admin File Manager is a whole-filesystem (deny-listed)
-- browser/editor on the admin side. It exposes root-owned paths + edits to the
-- panel UI, so it is opt-in and OFF by default — an explicit, deliberate enable.
ALTER TABLE server_settings
    ADD COLUMN admin_file_manager_enabled TINYINT(1) NOT NULL DEFAULT 0;
