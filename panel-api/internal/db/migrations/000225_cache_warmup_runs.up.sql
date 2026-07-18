-- JAB-95 Phase 3: persistent, queryable cache-warmup runs. Turns warmup from a
-- fire-and-forget goroutine into a durable record so the panel can show what a
-- warmup did (state, warmed/attempted/failed, first error, duration) and later
-- resume an interrupted one from cursor_pos. (`cursor` is a MySQL reserved word,
-- hence cursor_pos.)
CREATE TABLE cache_warmup_runs (
  id           CHAR(26) NOT NULL,
  install_id   CHAR(26) NOT NULL,
  host         VARCHAR(255) NOT NULL,
  state        VARCHAR(16) NOT NULL DEFAULT 'queued', -- queued|running|done|failed
  requested    INT NOT NULL DEFAULT 0,
  clamped_to   INT NOT NULL DEFAULT 0,
  total        INT NOT NULL DEFAULT 0,
  cursor_pos   INT NOT NULL DEFAULT 0,
  attempted    INT NOT NULL DEFAULT 0,
  warmed       INT NOT NULL DEFAULT 0,
  skipped      INT NOT NULL DEFAULT 0,
  failed       INT NOT NULL DEFAULT 0,
  first_error  VARCHAR(1024) NOT NULL DEFAULT '',
  started_at   TIMESTAMP NULL,
  finished_at  TIMESTAMP NULL,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_cwr_install_created (install_id, created_at),
  KEY idx_cwr_state (state)
);
