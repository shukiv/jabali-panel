-- GH #526: docroot editing gets its own switch, decoupled from the tenant nginx
-- Rule Builder so an admin can enable domain-confined docroot editing without
-- unlocking the riskier raw options. Default ON — the edit is confined to the
-- tenant's own /home/<user>/domains/<domain>/ tree, many apps (Laravel et al.)
-- need a subdir docroot, and peer panels (Hestia) expose it by default. Admins
-- who want it locked down can still toggle it off.
ALTER TABLE server_settings
  ADD COLUMN tenant_docroot_editable TINYINT(1) NOT NULL DEFAULT 1;
