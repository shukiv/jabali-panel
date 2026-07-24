-- Reverse JAB-170 shared certificates.
ALTER TABLE domains DROP FOREIGN KEY fk_domains_shared_cert;
ALTER TABLE domains DROP KEY ix_domains_shared_cert;
ALTER TABLE domains DROP COLUMN shared_certificate_id;
ALTER TABLE domains
    MODIFY COLUMN ssl_mode ENUM('le','self','custom','none') NOT NULL DEFAULT 'le';
DROP TABLE shared_certificates;
