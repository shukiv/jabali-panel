-- GH #1701 Slice 3: two security-sensitive per-domain PHP directives, rendered
-- through fastcgi_param PHP_ADMIN_VALUE (they are PHP_INI_SYSTEM; PHP_VALUE
-- cannot set them).
--   php_open_basedir    — open_basedir: a colon-separated list of the tokens
--                         {WEBSPACEROOT}, {DOCROOT}, {TMP} and absolute paths
--                         (internal/phpbasedir validates it)
--   php_allow_url_fopen — allow_url_fopen
-- Both NULL = inherit (the pool's open_basedir; the box php.ini's
-- allow_url_fopen). The agent pins both on every PHP vhost, so a value set on
-- one domain cannot stick on a reused FPM worker and bleed onto a sibling
-- domain on the same pool. TEXT, not VARCHAR, keeps the domains row clear of
-- the InnoDB row-size ceiling.
ALTER TABLE domains
  ADD COLUMN php_open_basedir TEXT NULL DEFAULT NULL,
  ADD COLUMN php_allow_url_fopen TINYINT(1) NULL DEFAULT NULL;
