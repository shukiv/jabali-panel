-- GH #1175: shared loopback-port allocator.
--
-- Today each feature that needs a local host port hand-partitions its own
-- range in its own table (docker apps: docker_app_published_ports, 10000-19999;
-- python apps: 20000-29999). Adding reverse-proxy domains as a third silo would
-- compound that. This central registry is the one pool every current and future
-- local-port consumer draws from: one row = one port reserved for one owner on
-- one (bind_interface, protocol).
--
-- The DB UNIQUE on (bind_interface, port, protocol) makes the lowest-free
-- allocator race-safe: two concurrent allocations that pick the same port under
-- SELECT ... FOR UPDATE, or via a lost update, fail the second INSERT instead of
-- double-binding. owner_kind + owner_id let a consumer release its port(s) on
-- delete and look up "which port did I get".
--
-- Incremental adoption (ADR TBD): reverse_proxy uses this immediately; docker
-- and python migrate onto it as follow-ups, reserving their existing ranges
-- during the transition so live allocations never collide.
CREATE TABLE port_allocations (
  id             CHAR(26)     NOT NULL PRIMARY KEY,
  port           INT UNSIGNED NOT NULL,
  bind_interface VARCHAR(64)  NOT NULL DEFAULT '127.0.0.1',
  protocol       VARCHAR(8)   NOT NULL DEFAULT 'tcp',
  -- owner_kind: 'reverse_proxy' | 'docker_app' | 'python_app' | ...
  owner_kind     VARCHAR(32)  NOT NULL,
  owner_id       VARCHAR(64)  NOT NULL,
  created_at     DATETIME     NOT NULL,
  UNIQUE KEY uniq_portalloc_global (bind_interface, port, protocol),
  KEY ix_portalloc_owner (owner_kind, owner_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
