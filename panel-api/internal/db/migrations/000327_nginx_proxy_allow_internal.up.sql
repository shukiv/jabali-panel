-- The Rule Builder's proxy_pass rule may point at a loopback / private-range
-- address (or localhost) when this is on. Off by default, which keeps the
-- SSRF guard from JAB-65 exactly as it was. Unix sockets, link-local
-- (cloud metadata) and unspecified addresses stay blocked either way.
ALTER TABLE server_settings
  ADD COLUMN nginx_proxy_allow_internal TINYINT(1) NOT NULL DEFAULT 0;
