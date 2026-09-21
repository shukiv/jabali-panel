-- GH #1641: operator-set per-host AppSec mode.
--
-- A host with a row here of mode 'detect' is put into detection-only: the CRS
-- rules still run and score (explain keeps working) but the anomaly-score block
-- is suppressed for that host. This is the coarse companion to the per-path
-- exclusions in crs_rule_exclusions (JAB-227) — the right tool for a host whose
-- ordinary traffic trips a rotating set of rules (e.g. a Flarum forum where
-- users legitimately paste SQL/shell snippets into post bodies).
--
-- Host is UNIQUE: a host has at most one mode. Unlike an exclusion this
-- deliberately drops the anomaly-score blockers (949110/949111/980170), so it is
-- rendered from its own surface with its own validator (appseccfg.ValidateHostMode).
CREATE TABLE crs_host_modes (
  id          varchar(26)  NOT NULL,
  host        varchar(253) NOT NULL,
  mode        varchar(16)  NOT NULL,
  note        varchar(512) NOT NULL DEFAULT '',
  created_at  datetime(3)  NOT NULL,
  updated_at  datetime(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_crs_host_mode (host)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
