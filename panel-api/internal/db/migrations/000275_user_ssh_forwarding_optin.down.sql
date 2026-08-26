-- Revert GH #1229 per-user SSH-forwarding opt-in flag.
ALTER TABLE users
    DROP COLUMN ssh_forwarding_enabled;
