ALTER TABLE backup_destinations
    DROP COLUMN consecutive_failures,
    DROP COLUMN backoff_until;
