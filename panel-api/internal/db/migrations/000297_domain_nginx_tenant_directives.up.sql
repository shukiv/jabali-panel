-- GH #1624 / ADR-0169 Phase 4: tenant "advanced nginx directives".
--
-- domains.nginx_tenant_directives holds a tenant-authored raw nginx snippet
-- injected into the server block, kept SEPARATE from the admin-only
-- nginx_custom_directives. It is validated at the API boundary with a tight
-- tenant value-grammar (ValidateNginxDirectivesTenant): a single statement per
-- line, no blocks, and only add_header / expires / etag — so it cannot SSRF,
-- disclose files, or suppress logging the way the admin raw field can. Gated on
-- server_settings.tenant_domain_options_enabled at write time, mirroring the
-- typed Rule Builder subset and the safe-options toggle.
ALTER TABLE domains
    ADD COLUMN nginx_tenant_directives TEXT NULL;
