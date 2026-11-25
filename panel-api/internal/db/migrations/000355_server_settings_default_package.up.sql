-- Default package for migrated accounts: a migration that creates the
-- destination user without a target package gives it this package. NULL = no
-- default (the user gets no package, the GH #282 behaviour). Set in Server
-- Settings -> General; deleting the package clears it.
--
-- TEXT for the server_settings row-size ceiling (GH #1766). No foreign key:
-- users.package_id has none either, and the package repository clears this
-- column when it deletes the package.
ALTER TABLE server_settings
  ADD COLUMN default_package_id TEXT NULL;

-- Existing servers start on the seeded "Tier 3" when they still have it.
-- On a fresh install hosting_packages is still empty here (the panel seeds
-- Tier 1/2/3 at its first start, after the migrations), so this matches
-- nothing; serve.go sets it when it first creates the settings row.
UPDATE server_settings
  SET default_package_id = (SELECT id FROM hosting_packages WHERE name = 'Tier 3' ORDER BY id LIMIT 1)
  WHERE default_package_id IS NULL;
