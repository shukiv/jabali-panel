-- JAB-228: raise the default nginx client_max_body_size to match the PHP-FPM
-- pool, which has always allowed 512M.
--
-- install/php/jabali-php-pool.conf.tmpl sets upload_max_filesize=512M and
-- post_max_size=512M, while nginx capped request bodies at 50m. nginx therefore
-- rejected anything over 50MB with 413 BEFORE PHP ever saw it — while WordPress
-- read the PHP limits and advertised a much larger maximum to the user. Uploads
-- failed mid-transfer with an opaque error, and retries produced duplicate
-- attachments (name.jpg, name-1.jpg, name-2.jpg).
--
-- Only rows still holding the old default are moved. An operator who has
-- deliberately set any other value keeps it — this is a wrong default, not a
-- policy to override.
UPDATE server_settings
   SET nginx_client_max_body_size = '512m'
 WHERE nginx_client_max_body_size = '50m';

ALTER TABLE server_settings
  MODIFY COLUMN nginx_client_max_body_size varchar(16) NOT NULL DEFAULT '512m';
