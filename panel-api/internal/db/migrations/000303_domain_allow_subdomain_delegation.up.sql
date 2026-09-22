-- GH #1812 (follow-up to #1789): per-domain subdomain delegation.
--
-- #1789 added CrossTenantSuffixCollision, which blocks a tenant from
-- self-servicing a domain that is a strict subdomain (or parent) of a domain
-- owned by a DIFFERENT tenant — the DNS/mail hijack path. That guard has no
-- notion of the parent's owner CONSENTING to the nesting, so the only
-- legitimate delegated arrangement today is an admin-provisioned subdomain or
-- same-owner nesting.
--
-- This column is that consent, held on the PARENT domain row and set by its
-- owner. When allow_subdomain_delegation = 1, another tenant may self-service a
-- strict subdomain of this domain; the child never claims a parent over it (the
-- grant is parent -> its subdomains only). DEFAULT 0 so every existing row and
-- every non-opting owner stays protected exactly as #1789 enforces it.
ALTER TABLE domains
  ADD COLUMN allow_subdomain_delegation tinyint(1) NOT NULL DEFAULT 0;
