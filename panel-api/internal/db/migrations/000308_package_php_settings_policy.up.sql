-- GH #1701 slice 1: per-package PHP settings policy. A JSON object of
-- php.ini directive -> level (admin_only / tenant_allowed /
-- tenant_privileged) saying who may set that directive on the per-domain
-- PHP Settings page. '' = every directive at its catalog default, which for
-- the directives that page writes today is tenant_allowed: existing
-- packages keep exactly what their tenants can do now.
ALTER TABLE hosting_packages
  ADD COLUMN php_settings_policy TEXT NOT NULL DEFAULT '';
