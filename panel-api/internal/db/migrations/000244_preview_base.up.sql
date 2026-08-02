-- GH #836: configurable preview-URL base. Empty = derive
-- preview.<hostname> (the default behavior). Set it to a custom domain
-- (preview.example.com) or a magic-DNS base (203-0-113-7.sslip.io) on
-- boxes whose hostname does not resolve publicly.
ALTER TABLE server_settings
  ADD COLUMN preview_base varchar(253) NOT NULL DEFAULT '';
