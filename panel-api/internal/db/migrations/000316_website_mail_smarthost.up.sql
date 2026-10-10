-- GH #2056: send website mail (PHP mail()) through the operator's smarthost
-- instead of the local mail server, so a server without the mail module can
-- still send its sites' contact-form mail. See ADR 0174.
--
-- website_mail_mode: 'local' (today's behavior: the jabali-sendmail shim
-- submits to the local Stalwart) or 'smarthost'.
-- smarthost_tls: 'starttls' (required, not opportunistic), 'tls' (implicit
-- TLS) or 'none' (no login allowed).
-- smarthost_password_enc: the login password sealed with the panel's sso.key
-- (AES-256-GCM), never stored in plaintext and never returned by the API.
--
-- server_settings sits at InnoDB's 65535-byte row ceiling (GH #1766, see
-- 000301), so the strings are TEXT and the password a BLOB: each costs only a
-- few in-row bytes. Verified on a clone of an upgraded box's table
-- (MariaDB 10.11, innodb_strict_mode=1). Defaults keep every existing box on
-- 'local', so nothing changes until an admin picks a smarthost.
ALTER TABLE server_settings
  ADD COLUMN website_mail_mode      TEXT NOT NULL DEFAULT 'local',
  ADD COLUMN smarthost_host         TEXT NOT NULL DEFAULT '',
  ADD COLUMN smarthost_port         INT  NOT NULL DEFAULT 587,
  ADD COLUMN smarthost_tls          TEXT NOT NULL DEFAULT 'starttls',
  ADD COLUMN smarthost_username     TEXT NOT NULL DEFAULT '',
  ADD COLUMN smarthost_password_enc BLOB NULL;
