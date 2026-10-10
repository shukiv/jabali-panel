-- GH #2017: senders a mailbox trusts. Mail from one of these addresses is not
-- treated as spam when it passes SPF or DMARC. The rows are the truth
-- (ADR-0042); the reconciler writes each mailbox's list into Stalwart as
-- contact cards in a "Trusted senders" address book, which Stalwart's spam
-- filter trusts (SpamSettings.trustContacts).
--
-- address is the canonical sender (mailaddr.CanonicaliseSender): lowercased,
-- punycoded domain, +tag kept. 320 covers 64 + 1 + 253 octets.
-- Schema only; no seed rows.
CREATE TABLE mailbox_trusted_senders (
  id         CHAR(26)     NOT NULL PRIMARY KEY,
  mailbox_id CHAR(26)     NOT NULL,
  address    VARCHAR(320) NOT NULL,
  created_at DATETIME(6)  NOT NULL,
  UNIQUE KEY uq_mailbox_trusted_sender (mailbox_id, address),
  CONSTRAINT fk_mts_mailbox
    FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
