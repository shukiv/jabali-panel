-- GH #2262: the PHP Defense ingest read the journal from the start of the
-- second it stopped in, so each pass stored that second's incidents again.
-- Remove the copies: rows equal to another in every column but id. The
-- oldest of each set stays, and a copy holds nothing the kept row lacks.
--
-- One incident can have tens of thousands of copies, so the kept row is
-- found by grouping (linear) rather than by joining each copy to every older
-- one. Text columns are grouped by their hash: GROUP BY compares only the
-- first max_sort_length bytes of a TEXT value.
DELETE i FROM snuffleupagus_incidents i
LEFT JOIN (
  SELECT MIN(id) AS keep_id
  FROM snuffleupagus_incidents
  GROUP BY ts, rule_name, action, source_ip, SHA2(request_uri, 256),
           php_version, domain_id, domain, SHA2(raw, 256)
) k ON k.keep_id = i.id
WHERE k.keep_id IS NULL;
