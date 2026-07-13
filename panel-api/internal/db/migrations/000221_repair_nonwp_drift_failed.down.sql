-- Irreversible data repair (GH #378): the pre-repair 'failed' state was itself
-- the bug, and the original per-row timestamps are not recoverable. No-op.
SELECT 1;
