-- Preview URLs (temp URLs): per-domain opt-in. When enabled, the domain's
-- vhost gains a <slug>.preview.<hostname> server block serving the same
-- docroot, and the reconciler maintains the *.preview.<hostname> wildcard
-- DNS record + shared ACME cert that back it.
ALTER TABLE domains
  ADD COLUMN temp_url_enabled tinyint(1) NOT NULL DEFAULT 0;
