-- JAB-11: durable, queryable fleet cache-maintenance runs. A cache-doctor /
-- repair / refresh sweep touches every cache-enabled WordPress install with one
-- agent call each, so it runs in the background and the panel polls this row
-- for progress + keeps a history. Mirrors cache_warmup_runs (000225).
CREATE TABLE cache_doctor_runs (
  id            CHAR(26) NOT NULL,
  kind          VARCHAR(16) NOT NULL,                    -- doctor|repair|refresh
  state         VARCHAR(16) NOT NULL DEFAULT 'queued',   -- queued|running|done|failed
  triggered_by  VARCHAR(26) NOT NULL DEFAULT '',
  total         INT NOT NULL DEFAULT 0,
  checked       INT NOT NULL DEFAULT 0,
  drifted       INT NOT NULL DEFAULT 0,
  repaired      INT NOT NULL DEFAULT 0,
  refreshed     INT NOT NULL DEFAULT 0,
  failed        INT NOT NULL DEFAULT 0,
  first_error   VARCHAR(1024) NOT NULL DEFAULT '',
  results       JSON NULL,
  started_at    TIMESTAMP NULL,
  finished_at   TIMESTAMP NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_cdr_created (created_at),
  KEY idx_cdr_state (state)
);
