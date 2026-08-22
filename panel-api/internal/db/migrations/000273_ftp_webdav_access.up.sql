-- GH #1146: WebDAV as a third access option on FTP/SFTP subaccounts, alongside
-- ftp_access / sftp_access. Default 0 (off) — opt-in per subaccount, and the
-- feature ships behind the same package cap + server module as the rest of #1053.
-- Bool column: DDL DEFAULT 0 is safe here (0 is the zero value, so no gorm
-- default-tag zero-value trap — the model field carries NO gorm default).
ALTER TABLE ftp_accounts
    ADD COLUMN webdav_access TINYINT(1) NOT NULL DEFAULT 0;
