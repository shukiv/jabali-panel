-- GH #2328: paths of a domain that skip the AppSec bot-detection challenge.
--
-- Admin-only, like domains.bot_challenge_exempt (migration 287): it lowers
-- protection on a shared server. Each row exempts requests to one path
-- prefix of the domain and www.<domain>, for the listed methods only when
-- methods is set (comma-separated, upper-case, sorted; '' = every method).
-- The prefix is matched against the decoded path without the query string.
-- The WAF, rate limits and CrowdSec IP decisions still apply.
--
-- The panel reconciler writes the rows to a state file every pass and the
-- agent renders them into jabali-bot-exempt.yaml.
CREATE TABLE bot_challenge_exclusions (
  id          CHAR(26)     NOT NULL,
  domain_id   CHAR(26)     NOT NULL,
  path_prefix VARCHAR(512) NOT NULL,
  methods     VARCHAR(64)  NOT NULL DEFAULT '',
  note        VARCHAR(512) NOT NULL DEFAULT '',
  created_at  DATETIME(3)  NOT NULL,
  updated_at  DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_bot_challenge_excl (domain_id, path_prefix, methods),
  CONSTRAINT fk_bot_challenge_excl_domain FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
