-- Not reversed: there is no record of each domain's prior webmail_enabled value,
-- and re-enabling webmail on down-migrate would be the wrong default anyway (it
-- would resurrect webmail a tenant had deliberately turned off). The per-user
-- users.webmail_enabled column is kept, so an older binary reading it after a
-- rollback still sees the same OFF intent. Intentional no-op — mirrors the
-- 000080/000171 backfill downs.
SELECT 1;
