-- ADR-0177: whether the SSH sandbox (SSH, cron, Composer runs) gets the
-- system log socket at /dev/log, so PHP Defense blocks in that PHP reach the
-- journal and the incidents list. Off by default: the admin opts in.
ALTER TABLE server_settings ADD COLUMN ssh_sandbox_syslog TINYINT(1) NOT NULL DEFAULT 0;
