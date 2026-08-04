-- JAB-227: operator-managed CRS false-positive exclusions.
--
-- Before this, excluding a CRS rule for one customer's endpoint meant editing a
-- file on the box by hand (and knowing that only ctl:ruleRemoveById actually
-- works in Coraza — the target-scoped forms are silent no-ops). Nothing survived
-- `jabali update` unless it was placed in a directory jabali does not manage.
--
-- Scope is deliberately mandatory: an exclusion must name BOTH a host and a URI
-- prefix. ruleRemoveById drops the rule for the whole matched request, so an
-- unscoped entry would be a WAF hole for the entire server.
CREATE TABLE crs_rule_exclusions (
  id          varchar(26)  NOT NULL,
  host        varchar(253) NOT NULL,
  uri_prefix  varchar(512) NOT NULL,
  rule_id     varchar(16)  NOT NULL,
  note        varchar(512) NOT NULL DEFAULT '',
  created_at  datetime(3)  NOT NULL,
  updated_at  datetime(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_crs_excl (host, uri_prefix, rule_id),
  KEY idx_crs_excl_host (host)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
