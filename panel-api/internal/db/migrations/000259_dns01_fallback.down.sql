ALTER TABLE server_settings
  DROP COLUMN cf_api_token_enc;

ALTER TABLE ssl_certificates
  DROP COLUMN issue_method;
