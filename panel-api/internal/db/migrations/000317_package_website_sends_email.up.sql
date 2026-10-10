-- GH #2056: "Website sends email" becomes a Hosting Package entitlement. It
-- decides whether the sites of users on the package may send mail with PHP
-- mail(), through the local mail server or the operator's smarthost. See
-- ADR 0174.
--
-- Existing packages get it ON so nothing changes for existing sites; packages
-- created from now on start with it OFF (the column default). Users with no
-- package keep sending through the local mail server but never through the
-- smarthost (GH #282: a privileged feature).
--
-- The UPDATE is last, after the schema change (DML last).
ALTER TABLE hosting_packages
  ADD COLUMN website_sends_email TINYINT(1) NOT NULL DEFAULT 0;

UPDATE hosting_packages SET website_sends_email = 1;
