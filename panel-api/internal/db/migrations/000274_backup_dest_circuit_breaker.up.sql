-- JAB-362: per-destination circuit-breaker for the backup dispatcher. A
-- destination that keeps stall-failing (4h/attempt) is backed off from dispatch
-- for an exponential window instead of burning a shared concurrency slot every
-- tick. consecutive_failures drives the backoff length; backoff_until is the
-- skip-until timestamp (NULL = healthy). Both reset on the next successful seal.
-- INT + nullable DATETIME on a small table — no row-ceiling concern.
ALTER TABLE backup_destinations
    ADD COLUMN consecutive_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN backoff_until DATETIME(6) NULL;
