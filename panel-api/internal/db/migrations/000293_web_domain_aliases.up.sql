-- GH #1625: web domain aliases.
--
-- An alias is an additional hostname served from the SAME vhost and
-- docroot as its owning web domain: the reconciler adds it to the main
-- server block's server_name and — when it resolves DIRECTLY to this
-- server — to the domain's TLS certificate as a SAN. Deleting the
-- domain removes its aliases (ON DELETE CASCADE); the per-tick domain
-- reconcile then re-renders the vhost without them.
--
-- hostname is globally UNIQUE (one alias name maps to exactly one
-- domain), matching the ux_domains_name constraint on primary names.
-- The create handler additionally rejects an alias that collides with
-- any domains.name, the domain's own apex/www, or another tenant's
-- mail/web helper server_name — a duplicate server_name across two
-- nginx server blocks silently lets the first win (cross-tenant vhost
-- hijack), which the UNIQUE index alone does not catch.
--
-- Same charset/collation as domains (utf8mb4_unicode_ci): a foreign key
-- requires both columns to share a collation (MariaDB errno 150
-- otherwise).
CREATE TABLE web_domain_aliases (
    id          CHAR(26)     NOT NULL PRIMARY KEY,
    domain_id   CHAR(26)     NOT NULL,
    hostname    VARCHAR(253) NOT NULL,
    created_at  DATETIME(6)  NOT NULL,
    updated_at  DATETIME(6)  NOT NULL,
    UNIQUE INDEX ux_web_domain_aliases_hostname (hostname),
    INDEX idx_web_domain_aliases_domain (domain_id),
    CONSTRAINT fk_web_domain_aliases_domain
        FOREIGN KEY (domain_id) REFERENCES domains (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
