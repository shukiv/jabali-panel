ALTER TABLE server_settings
  MODIFY COLUMN nginx_client_max_body_size varchar(16) NOT NULL DEFAULT '50m';

UPDATE server_settings
   SET nginx_client_max_body_size = '50m'
 WHERE nginx_client_max_body_size = '512m';
