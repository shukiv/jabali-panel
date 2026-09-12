-- Revert GH #1624 / ADR-0169 Phase 4: drop the tenant advanced-directives column.
ALTER TABLE domains
    DROP COLUMN nginx_tenant_directives;
