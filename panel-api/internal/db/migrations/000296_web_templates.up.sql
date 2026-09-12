-- GH #1624 / ADR-0169 Phase 3: admin-managed web (nginx) templates.
--
-- A web template is a named, admin-defined preset of raw nginx directives — the
-- "copy my working config" migration vehicle (@johnnyq, #1624). An admin authors
-- the directives (validated by the same relaxed denylist the admin custom-
-- directives PATCH uses, ValidateNginxDirectivesAdmin); at Web Domain create an
-- ADMIN may select a template and its directives are snapshot-copied onto the
-- new domain's nginx_custom_directives. Snapshot, not a live link: later template
-- edits do not propagate; the admin edits the domain directly (or re-creates).
--
-- ADMIN-SELECT-ONLY in this phase. The admin denylist (root/alias/include/...)
-- does NOT block proxy_pass, so a globally-visible template holding
-- `proxy_pass http://127.0.0.1:...` picked by a different tenant would front a
-- localhost service from that tenant's own domain — the ADR-0169 SSRF threat with
-- the admin as unwitting author. Per-template tenant-selectable opt-in is a later
-- phase.
--
-- Single table: nginx directives are one raw text blob, not N structured records
-- like a DNS template (#1627), so there is no child table.
--
-- Migration-number note: 000295 (dns_orphan_autosweep) is the highest on main at
-- authoring time. Whichever nginx/backup migration merges after this renumbers to
-- the next free version at rebase — the embedded-migrations completeness test
-- (contiguous, no duplicate version) catches a collision.

CREATE TABLE web_templates (
    id               CHAR(26)      NOT NULL PRIMARY KEY,
    name             VARCHAR(120)  NOT NULL,
    description      VARCHAR(500)  NOT NULL DEFAULT '',
    nginx_directives TEXT          NOT NULL,
    created_at       DATETIME(6)   NOT NULL,
    updated_at       DATETIME(6)   NOT NULL,
    UNIQUE INDEX ux_web_templates_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The domain records WHICH web template it was created from (nullable),
-- informational only — the directives are already snapshot-copied into
-- nginx_custom_directives at create, so nothing reads this at reconcile.
-- Deliberately NOT a foreign key (mirrors mail_template_id): a later template
-- delete leaves a harmless dangling id, and the big domains table gains no
-- cross-table constraint.
ALTER TABLE domains
    ADD COLUMN web_template_id CHAR(26) NULL;
