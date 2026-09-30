-- ADR-0110 (amendment 2026-09-29): postmaster@ on every domain but the panel
-- hostname's belongs to the server administrator.
--
-- Stalwart's SQL directory delivers postmaster@<domain>, for a domain with no
-- postmaster of its own, to the admin's postmaster mailbox on the panel domain,
-- and lists the address on that account (queryEmailAliases). Stalwart then
-- stores the address on the admin's account in its own registry and never
-- removes it, not even when the SQL no longer lists it. A tenant mailbox
-- created later at postmaster@<domain> signs in to the ADMIN's account
-- (verified on the test box: auth.success for the tenant's address and
-- password, accountId = the admin postmaster). A later alias, group or shared
-- resource at the address never receives its mail.
--
-- So no new row may take postmaster@ on a domain that is not the panel
-- hostname's. The doors check this first and answer with a clear error; these
-- triggers are the guarantee for every door (API, CLI, importers, backup
-- restore, direct SQL). Rows that already exist keep working: an UPDATE is
-- refused only when it MOVES a row onto postmaster@ (local part or domain).
-- The comparison uses the column collation (utf8mb4_unicode_ci), so any case
-- of "postmaster" matches.
--
-- The MESSAGE_TEXT is matched by the repository layer
-- (repository.mapPostmasterReserved); keep them identical.

CREATE TRIGGER trg_mailboxes_postmaster_reserved_insert
  BEFORE INSERT ON mailboxes
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_mailboxes_postmaster_reserved_update
  BEFORE UPDATE ON mailboxes
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_email_forwarders_postmaster_reserved_insert
  BEFORE INSERT ON email_forwarders
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_email_forwarders_postmaster_reserved_update
  BEFORE UPDATE ON email_forwarders
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_mail_groups_postmaster_reserved_insert
  BEFORE INSERT ON mail_groups
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_mail_groups_postmaster_reserved_update
  BEFORE UPDATE ON mail_groups
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_shared_resources_postmaster_reserved_insert
  BEFORE INSERT ON shared_resources
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;

CREATE TRIGGER trg_shared_resources_postmaster_reserved_update
  BEFORE UPDATE ON shared_resources
  FOR EACH ROW
  BEGIN
    IF NEW.local_part = 'postmaster'
       AND (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND NOT EXISTS (SELECT 1 FROM domains d WHERE d.id = NEW.domain_id AND d.is_panel_primary = 1) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'postmaster@ belongs to the server administrator';
    END IF;
  END;
