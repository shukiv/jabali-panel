ALTER TABLE domains DROP COLUMN mail_template_id;
DROP TABLE IF EXISTS dns_template_records;
DROP TABLE IF EXISTS dns_templates;
