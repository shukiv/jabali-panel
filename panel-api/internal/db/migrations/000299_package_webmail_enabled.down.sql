-- Reverse of 000299: drop the package webmail entitlement column.
ALTER TABLE hosting_packages
  DROP COLUMN webmail_enabled;
