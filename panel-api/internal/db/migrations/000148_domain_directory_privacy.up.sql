-- M50: per-directory password protection (cPanel "Directory Privacy").
--
-- One rule per (domain, path) — locks a subdirectory of the docroot
-- behind HTTP Basic Auth with a custom realm. N credentials per rule,
-- bcrypt-hashed at the API; agent writes a per-rule htpasswd file at
-- /etc/jabali-panel/dir-privacy/<rule_ulid>.htpasswd and the vhost
-- template emits `location ^~ <path>/ { auth_basic <realm>; ... }`.
--
-- Rule with zero credentials → agent writes a deny-all placeholder so
-- the location still 401s (safer than silently allowing).

CREATE TABLE domain_directory_privacy (
    id          CHAR(26)        NOT NULL PRIMARY KEY,
    domain_id   CHAR(26)        NOT NULL,
    path        VARCHAR(255)    NOT NULL,
    realm       VARCHAR(255)    NOT NULL DEFAULT 'Restricted',
    created_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    UNIQUE KEY uniq_domain_directory_privacy_path (domain_id, path),
    INDEX idx_domain_directory_privacy_domain (domain_id),
    CONSTRAINT fk_domain_directory_privacy_domain
        FOREIGN KEY (domain_id) REFERENCES domains (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE domain_directory_privacy_credentials (
    id              CHAR(26)        NOT NULL PRIMARY KEY,
    rule_id         CHAR(26)        NOT NULL,
    username        VARCHAR(64)     NOT NULL,
    password_hash   VARCHAR(120)    NOT NULL,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uniq_domain_directory_privacy_cred_user (rule_id, username),
    INDEX idx_domain_directory_privacy_cred_rule (rule_id),
    CONSTRAINT fk_domain_directory_privacy_cred_rule
        FOREIGN KEY (rule_id) REFERENCES domain_directory_privacy (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
