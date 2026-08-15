-- GH #1053 follow-up: operator-tunable vsftpd limits (Server Settings →
-- SSH & FTP). Fixed-width INT columns on purpose — the server_settings
-- InnoDB row-size ceiling (000262/000264 lesson) bites variable-length
-- columns; an INT adds a flat 4 bytes.
--
--   ftp_max_clients        total simultaneous FTP sessions (0 = unlimited)
--   ftp_max_per_ip         sessions per source IP (0 = unlimited)
--   ftp_local_max_rate_kbs per-session transfer cap in KB/s (0 = unlimited)
ALTER TABLE server_settings
    ADD COLUMN ftp_max_clients INT UNSIGNED NOT NULL DEFAULT 50,
    ADD COLUMN ftp_max_per_ip INT UNSIGNED NOT NULL DEFAULT 8,
    ADD COLUMN ftp_local_max_rate_kbs INT UNSIGNED NOT NULL DEFAULT 0;
