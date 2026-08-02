-- GH #648: opt-in DKIM2 chain-of-custody signing (draft-ietf-dkim-dkim2).
-- Default 0 while the spec is a working-group draft; the DNS side needs no
-- changes (DKIM2 reuses the published DKIM1 TXT records verbatim).
ALTER TABLE server_settings
  ADD COLUMN dkim2_signing_enabled tinyint(1) NOT NULL DEFAULT 0;
