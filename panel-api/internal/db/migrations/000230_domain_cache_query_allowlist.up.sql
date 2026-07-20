-- Per-domain cacheable query-param allowlist (cache-query-allowlist blueprint,
-- rec #1). Comma-joined, lower-cased, sorted param names (e.g. "paged"). Empty
-- string = feature off (the default) so existing domains are untouched: the
-- vhost render is byte-identical when this is empty. Only GET pages whose query
-- is entirely allowlisted (+ tracking/empty) params cache as distinct entries.
ALTER TABLE domains
  ADD COLUMN cache_query_allowlist VARCHAR(255) NOT NULL DEFAULT '';
