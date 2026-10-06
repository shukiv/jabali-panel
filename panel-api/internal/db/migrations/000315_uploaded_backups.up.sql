-- GH #1993: account backups uploaded from another server stay on this one.
--
-- An admin restore from an uploaded file deleted the file after every restore,
-- so a failed restore of a multi-GB archive meant uploading it again. The file
-- is now kept under /var/lib/jabali-uploads/kept/<id>.tar.zst (the path is
-- derived from the id, never stored) and listed in Backups, from where it can
-- be restored again or deleted.
--
--   file_name          the file name the admin uploaded, for display only
--   size_bytes         the archive's size on disk
--   account_username   the account the archive was taken from (display; every
--   account_email      restore inspects the archive again)
--   components         the restorable stages in the archive, comma-separated
--   retention          keep | keep_7_days | delete_after_restore
--   expires_at         when keep_7_days removes it (NULL = never)
--   uploaded_by        the admin who uploaded it (informational, no FK)
--   restore_status     '' | restoring | done | failed: the last restore
--   restore_started_at when the running or last restore started; a restoring
--                      row older than the restore deadline counts as stale
--   restored_at        when the last restore finished
--   restore_target     the account the last restore went into
--   restore_result     the last restore's report (JSON: applied, warnings, error)
--
-- DDL only, no backfill.
CREATE TABLE uploaded_backups (
  id                 CHAR(26)        NOT NULL PRIMARY KEY,
  file_name          VARCHAR(255)    NOT NULL DEFAULT '',
  size_bytes         BIGINT UNSIGNED NOT NULL DEFAULT 0,
  account_username   VARCHAR(64)     NOT NULL DEFAULT '',
  account_email      VARCHAR(255)    NOT NULL DEFAULT '',
  components         VARCHAR(255)    NOT NULL DEFAULT '',
  retention          VARCHAR(24)     NOT NULL DEFAULT 'keep',
  expires_at         DATETIME(6)     NULL,
  uploaded_by        CHAR(26)        NOT NULL DEFAULT '',
  restore_status     VARCHAR(16)     NOT NULL DEFAULT '',
  restore_started_at DATETIME(6)     NULL,
  restored_at        DATETIME(6)     NULL,
  restore_target     VARCHAR(64)     NOT NULL DEFAULT '',
  restore_result     TEXT            NULL,
  created_at         DATETIME(6)     NOT NULL,
  updated_at         DATETIME(6)     NOT NULL,
  INDEX ix_uploaded_backups_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
