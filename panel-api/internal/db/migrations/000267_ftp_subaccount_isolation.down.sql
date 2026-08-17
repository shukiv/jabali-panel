-- Reverse 000267: drop the subaccount-isolation columns + uid allocator.
DROP TABLE IF EXISTS ftp_subaccount_uid_seq;

ALTER TABLE ftp_accounts
    DROP INDEX ux_ftp_accounts_uid,
    DROP COLUMN uid,
    DROP COLUMN isolated,
    DROP COLUMN quota_mb,
    DROP COLUMN jail_path;
