-- GH #526: docroot editing gets its own switch, decoupled from the tenant nginx
-- Rule Builder so an admin can enable domain-confined docroot editing without
-- unlocking the riskier raw options. Default OFF (admin opt-in) — repointing a
-- docroot is still a foot-gun a tenant can use to expose their own files.
ALTER TABLE server_settings
  ADD COLUMN tenant_docroot_editable TINYINT(1) NOT NULL DEFAULT 0;
