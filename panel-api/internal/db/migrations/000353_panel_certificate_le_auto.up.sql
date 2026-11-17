-- The panel's Let's Encrypt certificate turns on by itself on a new install
-- (ADR-0066 amendment). le_auto_until is set on the hostname row when the
-- application creates it, which happens once, on a fresh install's first pass.
-- Until then the reconciler turns use_le on once public DNS shows the panel
-- hostname at this server. NULL everywhere else: existing servers keep their
-- setting, and an admin's toggle ends the window.
ALTER TABLE panel_certificate
  ADD COLUMN le_auto_until DATETIME NULL DEFAULT NULL;
