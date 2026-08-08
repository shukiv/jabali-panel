ALTER TABLE domains
  DROP INDEX idx_domains_ghost_checked_at,
  DROP INDEX idx_domains_registrar_checked_at;
