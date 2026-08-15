-- GH #1053: FTP/SFTP subaccounts (plans/gh1053-ftp-accounts.md).
--
-- Each row maps to a REAL system user created as a same-uid alias of the
-- owning tenant (useradd --non-unique), so every file written over any
-- protocol is owned by the tenant uid and quotas/FPM/AppArmor behave as if
-- the tenant wrote it. No password column ON PURPOSE: credentials live in
-- /etc/shadow only, set via the agent at create/reset time.
--
-- user_id is index-only (no FK) — same convention as ssh_keys; the panel
-- row is the teardown handle, so deletion must go through the app path
-- that also removes the system user, never a silent DB cascade.
CREATE TABLE ftp_accounts (
  id          CHAR(26)     NOT NULL PRIMARY KEY,
  user_id     CHAR(26)     NOT NULL,
  username    VARCHAR(64)  NOT NULL,
  home_path   VARCHAR(512) NOT NULL,
  ftp_access  TINYINT(1)   NOT NULL DEFAULT 0,
  sftp_access TINYINT(1)   NOT NULL DEFAULT 1,
  is_enabled  TINYINT(1)   NOT NULL DEFAULT 1,
  created_at  DATETIME(6)  NOT NULL,
  updated_at  DATETIME(6)  NOT NULL,
  UNIQUE INDEX ux_ftp_accounts_username (username),
  INDEX idx_ftp_accounts_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Server-level FTP opt-in (operator decision 2026-08-15: default OFF).
-- ftp_enabled installs/starts the vsftpd module on enable; while 0 the
-- daemon is absent and port 21 closed — SFTP accounts keep working.
-- ftp_allow_plaintext is a second explicit gate: even with FTP on, TLS is
-- required unless the operator deliberately allows legacy cleartext.
-- Small typed columns on purpose: server_settings is close to InnoDB's
-- 65535-byte row-size ceiling (see 000262).
-- ftp_pasv_address is TEXT, not VARCHAR: server_settings already sits at
-- InnoDB's 65535-byte in-row ceiling, and a utf8mb4 VARCHAR(64) (~258
-- in-row bytes) fails with ERROR 1118 "Row size too large" on a real box
-- (proven on testserver 2026-08-15). TEXT is stored off-page and costs
-- ~12 in-row bytes, so it fits. The two TINYINTs fit as-is.
ALTER TABLE server_settings
    ADD COLUMN ftp_enabled TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN ftp_allow_plaintext TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN ftp_pasv_address TEXT NOT NULL DEFAULT '';

-- Per-package cap on tenant-created accounts. 0 = feature hidden for users
-- on that package (tenant capabilities default OFF).
ALTER TABLE hosting_packages
    ADD COLUMN max_ftp_accounts INT UNSIGNED NOT NULL DEFAULT 0;
