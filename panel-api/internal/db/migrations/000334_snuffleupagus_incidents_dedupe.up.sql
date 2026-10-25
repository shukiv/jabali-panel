-- GH #2262: the PHP Defense ingest read the journal from the start of the
-- second it stopped in, so each pass stored that second's incidents again.
-- Remove the copies: rows equal to an older row in every column but id. The
-- oldest of each set stays, and a copy holds nothing the kept row lacks.
DELETE dup FROM snuffleupagus_incidents dup
JOIN snuffleupagus_incidents kept
  ON kept.ts = dup.ts
 AND kept.rule_name = dup.rule_name
 AND kept.action = dup.action
 AND kept.id < dup.id
 AND kept.source_ip <=> dup.source_ip
 AND kept.request_uri <=> dup.request_uri
 AND kept.php_version <=> dup.php_version
 AND kept.domain_id <=> dup.domain_id
 AND kept.raw <=> dup.raw;
