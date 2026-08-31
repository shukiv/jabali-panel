-- GH #1310-adjacent ("drfeed spam"): a per-account ignore list for ssh.login
-- notifications. A noisy service account — e.g. a disaster-recovery feed whose
-- rsync/backup loop logs in over SSH every cycle — can be silenced without
-- disabling ssh.login for everyone else. Stored as a newline/comma-separated
-- list of usernames. TEXT (not VARCHAR) so it lives off-page: the
-- server_settings row is already near the MariaDB row-size ceiling.
ALTER TABLE server_settings
  ADD COLUMN ssh_login_ignore_accounts TEXT NOT NULL DEFAULT ('');
