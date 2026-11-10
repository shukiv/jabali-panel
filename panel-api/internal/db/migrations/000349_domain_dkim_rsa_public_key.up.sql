-- GH #2475: every mail domain signs with an RSA key on selector jabalirsa
-- beside its Ed25519 key, because several large receivers don't verify
-- Ed25519 signatures. This column holds the RSA key's DNS TXT value, as
-- dkim_public_key holds the Ed25519 one. NULL until the reconciler or a mail
-- enable fills it in.
ALTER TABLE domains ADD COLUMN dkim_rsa_public_key TEXT NULL AFTER dkim_public_key;
