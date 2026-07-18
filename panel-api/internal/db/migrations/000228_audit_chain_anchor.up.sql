-- JAB-105: pruning the hash-chained audit_events tail would leave the new-oldest
-- surviving row's prev_hash pointing at a deleted row, so `audit verify` (which
-- recomputes from genesis "") would report the chain broken. Persist an anchor =
-- the row_hash of the newest sealed row that was pruned; verify then starts from
-- the anchor instead of "", keeping the surviving chain verifiable. NULL = never
-- pruned (chain roots at genesis).
ALTER TABLE server_settings
  ADD COLUMN audit_chain_anchor CHAR(64) NULL;
