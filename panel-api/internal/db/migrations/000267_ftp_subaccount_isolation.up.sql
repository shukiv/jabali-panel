-- GH #1145 / JAB-252 + JAB-253: true (separate-uid) isolation for FTP/SFTP
-- subaccounts.
--
-- The original #1053 model made each subaccount a same-uid alias
-- (useradd --non-unique) chrooted to the tenant home. Two escapes followed:
--   JAB-252 (#1145) — an SFTP credential scoped to a sub-directory is only
--     `internal-sftp -d`'d there; `..` walks up to the tenant-home chroot
--     and every sibling site/secret/SSH file under it.
--   JAB-253 — vsftpd re-resolves the tenant-MUTABLE passwd home at login, so
--     a retargeted in-home symlink chroots out of the intended tree.
-- Both are closed by giving each ISOLATED subaccount its own uid inside a
-- root-owned jail (the selected sub-tree bind-mounted in), with a per-uid
-- quota and ACLs. See plans/gh1145-ftp-true-isolation.md +
-- plans/gh1053-ftp-jail-redesign.md.
--
-- These columns are additive. Existing rows stay in the legacy shared-access
-- mode (uid NULL, isolated 0) until re-provisioned; the reconciler detects
-- and fails them closed rather than silently trusting the old chroot.
ALTER TABLE ftp_accounts
    -- Own uid for isolated accounts. NULL = legacy shared-uid alias (not yet
    -- migrated). UNIQUE so an isolated uid is never double-assigned; MySQL
    -- treats multiple NULLs as distinct, so all legacy rows coexist.
    ADD COLUMN uid       INT UNSIGNED NULL,
    -- Dual-mode: 1 = separate-uid, jailed, kernel-isolated; 0 = legacy
    -- shared-access alias. New accounts are created isolated at the app
    -- layer; the column default stays 0 so this ALTER never marks an
    -- existing row "isolated" that the agent has not actually jailed.
    ADD COLUMN isolated  TINYINT(1)   NOT NULL DEFAULT 0,
    -- Per-account disk cap (MB) for the split allocation: the tenant package
    -- quota is divided across the tenant uid + its isolated subaccount uids
    -- (Σ subaccounts + tenant ≤ package). 0 = unset; the app rejects an
    -- isolated account with quota_mb = 0 (an unlimited isolated uid is a
    -- disk-fill hole — a separate uid escapes the tenant's own quota).
    ADD COLUMN quota_mb  INT UNSIGNED NOT NULL DEFAULT 0,
    -- Root-owned jail path (/var/lib/jabali-ftp-jails/<tenant>/<label>) for
    -- isolated accounts. Stored, not merely derived, so the reconciler can
    -- fail a row closed when home_path is not under its jail or the jail has
    -- no live mount. Empty for legacy shared-access rows.
    ADD COLUMN jail_path VARCHAR(512) NOT NULL DEFAULT '',
    ADD UNIQUE INDEX ux_ftp_accounts_uid (uid);

-- Monotonic, never-reuse allocator for subaccount uids. Single-row high-water
-- mark: allocation is `UPDATE ftp_subaccount_uid_seq SET next_uid = next_uid+1`
-- then read (or SELECT ... FOR UPDATE) inside the create txn. NEVER
-- decremented on delete, so a freed uid is never reassigned while any file it
-- owns may still survive under the tenant home — a new account must never
-- silently inherit a dead account's files.
--
-- Base 1000000000 (1e9) sits ABOVE the rootless-container subuid delegation
-- ceiling. /etc/subuid hands each tenant a 65536-uid block from SUB_UID_MIN
-- (100000) upward, capped at SUB_UID_MAX (600100000 on Debian/Ubuntu). A uid
-- inside that span would collide with a tenant's user-namespace mapping — a
-- rootless container writing as that host uid would share identity + quota
-- with the FTP subaccount (found live on a box: a tenant's block was
-- 493216-558751, straddling a naive 500000 base). 1e9 is above the ceiling,
-- below the systemd invalid-uid line (2^31), with ~1.1e9 uids of headroom.
CREATE TABLE ftp_subaccount_uid_seq (
  id       TINYINT UNSIGNED NOT NULL PRIMARY KEY,
  next_uid INT UNSIGNED     NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
INSERT INTO ftp_subaccount_uid_seq (id, next_uid) VALUES (1, 1000000000);
