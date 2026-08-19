-- Reverse 000270: drop the tenant reverse-proxy port column.
ALTER TABLE domains
    DROP COLUMN reverse_proxy_port;
