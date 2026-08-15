ALTER TABLE hosting_packages
    DROP COLUMN max_ftp_accounts;

ALTER TABLE server_settings
    DROP COLUMN ftp_pasv_address,
    DROP COLUMN ftp_allow_plaintext,
    DROP COLUMN ftp_enabled;

DROP TABLE ftp_accounts;
