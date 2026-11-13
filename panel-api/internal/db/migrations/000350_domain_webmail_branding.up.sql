-- GH #2502: webmail branding per domain. Bulwark brands each web address it
-- is opened on (DOMAIN_BRANDING, matched on the browser's host), and every
-- mail domain has its own, mail.<domain>. One row per branded domain: the
-- text fields, and for each picture the short hash of the PNG panel-api
-- stored under /var/lib/jabali-panel/webmail-branding/<domain id>/ ('' = none).
-- Empty fields fall back to the server-wide branding.
--
-- set_by_admin: an administrator's branding applies whatever the owner's
-- package allows; the owner's own applies only while their package has
-- webmail_branding_enabled.
--
-- webmail_branding_enabled defaults to 0 for every package, existing ones
-- included: an administrator grants it.
CREATE TABLE domain_webmail_branding (
  domain_id     CHAR(26)     NOT NULL PRIMARY KEY,
  app_name      VARCHAR(64)  NOT NULL DEFAULT '',
  company_name  VARCHAR(128) NOT NULL DEFAULT '',
  website_url   VARCHAR(512) NOT NULL DEFAULT '',
  privacy_url   VARCHAR(512) NOT NULL DEFAULT '',
  imprint_url   VARCHAR(512) NOT NULL DEFAULT '',
  logo_light    VARCHAR(16)  NOT NULL DEFAULT '',
  logo_dark     VARCHAR(16)  NOT NULL DEFAULT '',
  favicon       VARCHAR(16)  NOT NULL DEFAULT '',
  set_by_admin  TINYINT(1)   NOT NULL DEFAULT 0,
  created_at    DATETIME(6)  NOT NULL,
  updated_at    DATETIME(6)  NOT NULL,
  CONSTRAINT fk_domain_webmail_branding_domain
    FOREIGN KEY (domain_id) REFERENCES domains(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE hosting_packages
  ADD COLUMN webmail_branding_enabled TINYINT(1) NOT NULL DEFAULT 0;
