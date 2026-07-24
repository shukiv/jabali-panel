-- JAB-170: shared wildcard / multi-SAN certificate — uploaded once, served from
-- many domains. `shared_certificates` holds one cert/key pair + parsed SANs;
-- `domains.shared_certificate_id` attaches a domain to one; `ssl_mode` gains
-- 'shared'. Reconciler treats 'shared' like 'custom' (never ACME/revoke) but
-- re-renders every attached vhost when the shared cert is replaced.
CREATE TABLE shared_certificates (
    id          CHAR(26)     NOT NULL,
    name        VARCHAR(255) NOT NULL,
    -- user_id NULL = server-wide (admin-owned, attachable to any domain);
    -- otherwise the owning tenant (attachable only to domains they own).
    user_id     CHAR(26)     NULL,
    cert_path   VARCHAR(512) NULL,
    key_path    VARCHAR(512) NULL,
    -- sans is a JSON array of the certificate's Subject Alternative Names,
    -- parsed at upload for wildcard-aware attach matching.
    sans        TEXT         NULL,
    expires_at  DATETIME(6)  NULL,
    created_at  DATETIME(6)  NOT NULL,
    updated_at  DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    KEY ix_shared_cert_user (user_id),
    KEY ix_shared_cert_expires (expires_at),
    CONSTRAINT fk_shared_cert_user FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE CASCADE
)
ENGINE = InnoDB
DEFAULT CHARSET = utf8mb4
COLLATE = utf8mb4_unicode_ci;

-- Add 'shared' to the ssl_mode enum (was 'le','self','custom','none').
ALTER TABLE domains
    MODIFY COLUMN ssl_mode ENUM('le','self','custom','none','shared') NOT NULL DEFAULT 'le';

-- Attach column + FK. Explicit COLLATE so the FK column matches
-- shared_certificates.id regardless of the domains table's own collation
-- (MariaDB FK requires an exact collation match).
ALTER TABLE domains
    ADD COLUMN shared_certificate_id CHAR(26) COLLATE utf8mb4_unicode_ci NULL,
    ADD KEY ix_domains_shared_cert (shared_certificate_id),
    ADD CONSTRAINT fk_domains_shared_cert FOREIGN KEY (shared_certificate_id)
        REFERENCES shared_certificates (id) ON DELETE SET NULL;
