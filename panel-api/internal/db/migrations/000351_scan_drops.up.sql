-- GH #2503: scanner uploads into a shared Files folder. A scanner logs in by
-- FTP or SFTP to its own login, homed at a drop folder in the domain's
-- directory (/home/<user>/domains/<domain>/scans/<name>, outside public_html).
-- panel-api moves each finished file from there into the shared resource's
-- Stalwart folder, where the members it is granted to see it in webmail.
--
-- One row per shared Files resource with a scanner. The row is the handle the
-- ingest loop sweeps by, so it has no foreign keys: when the resource is gone
-- (deleted, or its domain deleted) the loop removes the scanner login and then
-- the row; when the login was deleted on the FTP page, it removes the row.
--
-- max_mb caps the scans kept in the Stalwart folder (and is the login's disk
-- quota where logins are isolated). Stored scans are outside the account's
-- disk quota, so ingest stops at the cap until members delete scans.
CREATE TABLE scan_drops (
  resource_id     CHAR(26)        NOT NULL PRIMARY KEY,
  ftp_account_id  CHAR(26)        NOT NULL,
  drop_path       VARCHAR(512)    NOT NULL,
  max_mb          INT UNSIGNED    NOT NULL,
  ingested_total  BIGINT UNSIGNED NOT NULL DEFAULT 0,
  last_ingest_at  DATETIME(6)     NULL,
  last_error      VARCHAR(512)    NOT NULL DEFAULT '',
  created_at      DATETIME(6)     NOT NULL,
  updated_at      DATETIME(6)     NOT NULL,
  UNIQUE KEY ux_scan_drops_ftp_account (ftp_account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Members of a scans folder read and delete scans but cannot add or rename
-- anything: a grant level for Files resources only (the panel refuses it on
-- calendars and address books).
ALTER TABLE shared_resource_grants
  MODIFY rights ENUM('read','readwrite','admin','readdelete') NOT NULL DEFAULT 'read';
