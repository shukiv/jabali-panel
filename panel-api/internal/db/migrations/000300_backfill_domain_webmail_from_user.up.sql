-- GH #1628 (slice 3): backfill the per-domain webmail flag from the retired
-- per-user webmail flag. Slice 3 removes the per-user webmail toggle (#316) and
-- stops the reconciler AND the webmail SSO gate from reading users.webmail_enabled.
-- Without this backfill, a user who had webmail OFF at the user level would
-- silently get it back on every one of their domains once those readers are
-- gone. Copying the OFF state down to the domain rows preserves that intent at
-- the domain level, which the reconciler (domains.webmail_enabled) and the SSO
-- gate both still enforce.
--
-- Applied to ALL of the user's domains, not only email-enabled ones: setting
-- webmail_enabled=0 on a domain with email disabled is harmless (no mail vhost
-- exists to serve webmail), and it preserves the OFF intent for a domain whose
-- email is enabled LATER.
--
-- Pure DML, no schema change: domains.webmail_enabled has existed since #316,
-- and users.webmail_enabled is KEPT (not dropped this slice), so this read is
-- safe and an older binary still reads a consistent value on rollback. On a
-- fresh install with no users/domains this affects 0 rows.
UPDATE domains d
  JOIN users u ON d.user_id = u.id
  SET d.webmail_enabled = 0
  WHERE u.webmail_enabled = 0;
