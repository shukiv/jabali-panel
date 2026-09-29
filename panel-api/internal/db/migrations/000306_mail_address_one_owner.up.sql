-- A mailbox never shares its address with an alias, a mail group or a shared
-- resource in the same domain.
--
-- Stalwart looks an address up in its own registry before the SQL
-- directory, and the registry keeps every alias it has ever seen on the
-- account that owned it. A mailbox created at an address that is, or was, an
-- alias of another mailbox therefore signed in to THAT mailbox's account
-- (verified on the test box, 2026-09-29: mailbox sales@ and mailbox info@
-- both got auth.success with the CEO's accountId, one after its alias was
-- deleted, one while the alias still existed). A mailbox at a mail group's
-- address could not sign in at all.
--
-- These triggers refuse a new mailbox at an address an alias, group or
-- shared resource holds, and a new alias, group or shared resource at an
-- address a mailbox holds. An alias counts whether it is enabled or not: a
-- disabled alias turned back on would put the address on its mailbox again.
-- An UPDATE is refused only when it moves a row onto such an address, so
-- existing rows keep working. Pairs that already exist are handled by the
-- SQL directory queries (a mailbox wins) and the panel's registry sweep
-- (mailaddrowner). A stale registry alias left by a deleted alias is
-- cleared by the mailbox create doors before the row is written.
--
-- The comparison uses the column collation (utf8mb4_unicode_ci), so case does
-- not matter. The MESSAGE_TEXT is matched by repository.mapAddressInUse; keep
-- them identical.

CREATE TRIGGER trg_mailboxes_one_owner_insert
  BEFORE INSERT ON mailboxes
  FOR EACH ROW
  BEGIN
    IF EXISTS (SELECT 1 FROM email_forwarders f WHERE f.domain_id = NEW.domain_id AND f.type = 'alias' AND f.local_part = NEW.local_part)
       OR EXISTS (SELECT 1 FROM mail_groups g WHERE g.domain_id = NEW.domain_id AND g.local_part = NEW.local_part)
       OR EXISTS (SELECT 1 FROM shared_resources s WHERE s.domain_id = NEW.domain_id AND s.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_mailboxes_one_owner_update
  BEFORE UPDATE ON mailboxes
  FOR EACH ROW
  BEGIN
    IF (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND (EXISTS (SELECT 1 FROM email_forwarders f WHERE f.domain_id = NEW.domain_id AND f.type = 'alias' AND f.local_part = NEW.local_part)
         OR EXISTS (SELECT 1 FROM mail_groups g WHERE g.domain_id = NEW.domain_id AND g.local_part = NEW.local_part)
         OR EXISTS (SELECT 1 FROM shared_resources s WHERE s.domain_id = NEW.domain_id AND s.local_part = NEW.local_part)) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_email_forwarders_one_owner_insert
  BEFORE INSERT ON email_forwarders
  FOR EACH ROW
  BEGIN
    IF NEW.type = 'alias'
       AND EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_email_forwarders_one_owner_update
  BEFORE UPDATE ON email_forwarders
  FOR EACH ROW
  BEGIN
    IF NEW.type = 'alias'
       AND (NOT (OLD.type <=> NEW.type) OR NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_mail_groups_one_owner_insert
  BEFORE INSERT ON mail_groups
  FOR EACH ROW
  BEGIN
    IF EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_mail_groups_one_owner_update
  BEFORE UPDATE ON mail_groups
  FOR EACH ROW
  BEGIN
    IF (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_shared_resources_one_owner_insert
  BEFORE INSERT ON shared_resources
  FOR EACH ROW
  BEGIN
    IF EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;

CREATE TRIGGER trg_shared_resources_one_owner_update
  BEFORE UPDATE ON shared_resources
  FOR EACH ROW
  BEGIN
    IF (NOT (OLD.local_part <=> NEW.local_part) OR NOT (OLD.domain_id <=> NEW.domain_id))
       AND EXISTS (SELECT 1 FROM mailboxes m WHERE m.domain_id = NEW.domain_id AND m.local_part = NEW.local_part) THEN
      SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'the address already belongs to an alias, group or mailbox';
    END IF;
  END;
