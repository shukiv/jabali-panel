-- See 000306_mail_address_one_owner.up.sql.
DROP TRIGGER IF EXISTS trg_shared_resources_one_owner_update;
DROP TRIGGER IF EXISTS trg_shared_resources_one_owner_insert;
DROP TRIGGER IF EXISTS trg_mail_groups_one_owner_update;
DROP TRIGGER IF EXISTS trg_mail_groups_one_owner_insert;
DROP TRIGGER IF EXISTS trg_email_forwarders_one_owner_update;
DROP TRIGGER IF EXISTS trg_email_forwarders_one_owner_insert;
DROP TRIGGER IF EXISTS trg_mailboxes_one_owner_update;
DROP TRIGGER IF EXISTS trg_mailboxes_one_owner_insert;
