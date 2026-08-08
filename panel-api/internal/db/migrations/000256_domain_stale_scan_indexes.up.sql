-- Index the two per-tick staleness scans over `domains`.
--
-- ListForGhostCheck filters on `ghost_checked_at` alone, but the only index
-- covering that column is idx_domains_ghost_state (ghost_state,
-- ghost_checked_at) — its LEFTMOST column never appears in the predicate, so
-- MariaDB cannot use it and the query full-scans `domains` on every tick.
-- ListForRegistrarRefresh filters the same way on `registrar_checked_at`,
-- which had no index at all.
--
-- Both queries are `col IS NULL OR col < ?` ordered by the same column, which
-- a single-column index serves for both the range and the sort.
-- idx_domains_ghost_state is left in place: it still serves lookups that DO
-- filter on ghost_state.
ALTER TABLE domains
  ADD INDEX idx_domains_ghost_checked_at (ghost_checked_at),
  ADD INDEX idx_domains_registrar_checked_at (registrar_checked_at);
