-- JAB-390 slice B: the shared panel mail hostname switchover request.
--
-- server_settings.mail_hostname (000301) is the APPLIED panel mail hostname,
-- written only by the reconciler once a switchover has converged. This
-- singleton holds the DESIRED name an admin asked for and the switchover's
-- progress, so a name is never applied before it is routable and carries a
-- certificate:
--
--   desired       the requested name (NULL = no request)
--   status        idle | pending | issuing | failed | done
--   last_error    why the last attempt failed ('' when none)
--   attempts      attempts since the request
--   next_retry_at when a failed attempt is retried
--   requested_by  who asked (audit actor)
--
-- A separate table, not server_settings columns: server_settings is at
-- InnoDB's in-row size ceiling (GH #1766). Singleton: id=1 enforced by CHECK,
-- as panel_certificate does. The application creates the row on the first
-- request; this migration only creates the table. DDL only, no backfill.
CREATE TABLE mail_hostname_switchover (
  id            TINYINT UNSIGNED NOT NULL DEFAULT 1,
  desired       VARCHAR(253)     NULL,
  status        VARCHAR(16)      NOT NULL DEFAULT 'idle',
  last_error    VARCHAR(1024)    NOT NULL DEFAULT '',
  attempts      INT UNSIGNED     NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3)      NULL,
  requested_by  VARCHAR(64)      NOT NULL DEFAULT '',
  requested_at  DATETIME(3)      NULL,
  updated_at    DATETIME(3)      NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT mail_hostname_switchover_singleton CHECK (id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
