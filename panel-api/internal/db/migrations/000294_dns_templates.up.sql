-- GH #1627: admin-managed custom DNS templates.
--
-- A DNS template is a named, admin-defined preset of DNS records — the stored
-- equivalent of the built-in Jabali / Microsoft 365 / Google Workspace mail
-- presets that live in code (dnscompile). A tenant may select a template when
-- creating a Web Domain or DNS Zone; the reconciler seeds the template's
-- records ONCE into the fresh zone (managed=0, tenant-editable — never
-- re-asserted), and the domain is created with the external-mail posture
-- (mail_provider='custom'), so the mail-provider reconciler hands the zone off
-- entirely rather than pruning the template's own apex MX/SPF.
--
-- Templates are GLOBAL in this phase (visible to every tenant); per-package
-- assignment is a later phase. Record content is validated at admin-create
-- with the same DNS record validator the tenant record API uses.
--
-- Migration-number note (GH #1627): 000293 also numbers the unmerged web-domain
-- aliases migration (#1634). Whichever of the two merges SECOND renumbers to the
-- next free version at rebase — the embedded-migrations completeness test
-- (contiguous, no duplicate version) catches the collision.

CREATE TABLE dns_templates (
    id          CHAR(26)        NOT NULL PRIMARY KEY,
    name        VARCHAR(120)    NOT NULL,
    description VARCHAR(500)    NOT NULL DEFAULT '',
    created_at  DATETIME(6)     NOT NULL,
    updated_at  DATETIME(6)     NOT NULL,
    UNIQUE INDEX ux_dns_templates_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE dns_template_records (
    id          CHAR(26)        NOT NULL PRIMARY KEY,
    template_id CHAR(26)        NOT NULL,
    name        VARCHAR(255)    NOT NULL,
    type        VARCHAR(16)     NOT NULL,
    content     VARCHAR(4096)   NOT NULL,
    ttl         INT             NOT NULL DEFAULT 3600,
    priority    INT             NOT NULL DEFAULT 0,
    sort_order  INT             NOT NULL DEFAULT 0,
    created_at  DATETIME(6)     NOT NULL,
    updated_at  DATETIME(6)     NOT NULL,
    INDEX idx_dns_template_records_template (template_id, sort_order),
    CONSTRAINT fk_dns_template_records_template
        FOREIGN KEY (template_id) REFERENCES dns_templates (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The domain records WHICH template it was created from (nullable), so the
-- reconciler can seed that template's records the first time it bootstraps the
-- zone. Deliberately NOT a foreign key: it is an informational "created-from"
-- reference read exactly once at zone bootstrap; a later template delete leaves
-- a harmless dangling id (the reconciler's FindByID returns not-found → seeds
-- nothing), so no ON DELETE behaviour is needed and the big domains table
-- gains no cross-table constraint.
ALTER TABLE domains
    ADD COLUMN mail_template_id CHAR(26) NULL;
