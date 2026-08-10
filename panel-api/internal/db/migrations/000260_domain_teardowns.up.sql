-- JAB-236: durable host-side teardown for deleted domains.
--
-- Deleting a domain removes the panel row — the only handle — before the
-- host-side teardown (nginx vhost, pdns zone, Stalwart accounts) runs. A
-- fire-and-forget goroutine (REST/cascade) is lost on panel restart, and
-- the CLI dispatched nothing at all, leaving deleted domains SERVING.
-- This table is the tombstone: written BEFORE the row delete, removed only
-- after the teardown verifiably succeeds; the reconciler retries pending
-- rows every sweep. domain_name is the primary key so tombstone creation
-- is naturally idempotent.
CREATE TABLE domain_teardowns (
  domain_name     varchar(255) NOT NULL PRIMARY KEY,
  attempts        int          NOT NULL DEFAULT 0,
  last_error      varchar(1024) NOT NULL DEFAULT '',
  last_attempt_at datetime(6)  NULL,
  created_at      datetime(6)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
