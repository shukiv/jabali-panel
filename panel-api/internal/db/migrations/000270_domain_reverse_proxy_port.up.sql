-- GH #1175: a tenant reverse-proxy domain proxies '/' to a panel-ALLOCATED
-- loopback port (from port_allocations, owner_kind='reverse_proxy', migration
-- 000269). 0 = a normal docroot/PHP domain (the default — no existing row
-- changes). The port is panel-owned and never tenant-typed, so it sidesteps
-- the JAB-65 SSRF block that (correctly) rejects arbitrary loopback proxy_pass
-- targets from tenants.
ALTER TABLE domains
    ADD COLUMN reverse_proxy_port INT UNSIGNED NOT NULL DEFAULT 0;
