-- GH #2131: secondary DNS servers besides ns2_ipv4. Comma-separated IP
-- addresses that may transfer every hosted zone (AXFR) and are sent a NOTIFY
-- when one changes, so the panel can run as a hidden primary for a secondary
-- DNS provider. TEXT for the server_settings row-size ceiling (GH #1766).

ALTER TABLE server_settings
    ADD COLUMN dns_secondary_ips TEXT NOT NULL DEFAULT ('');
