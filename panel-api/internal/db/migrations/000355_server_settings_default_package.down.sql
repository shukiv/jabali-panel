-- Drop the default package for migrated accounts. Users already created with
-- it keep their package; only the setting goes.
ALTER TABLE server_settings
  DROP COLUMN default_package_id;
