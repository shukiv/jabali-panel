-- GH #465: server-wide account docroot skeleton. Each row is one starter file
-- (relative path + raw bytes) copied into a new domain's web docroot on
-- creation, generalizing the single default index.html the panel already
-- writes. Schema only — an empty table is the pre-#465 behaviour, so no seed
-- (feedback_migration_data_seed_ordering).
--
-- rel_path is VARCHAR(512): a utf8mb4 unique key must stay under InnoDB's
-- 3072-byte index limit (512 * 4 = 2048).
CREATE TABLE account_skeleton_files (
  id         CHAR(26)      NOT NULL PRIMARY KEY,
  rel_path   VARCHAR(512)  NOT NULL,
  content    LONGBLOB      NOT NULL,
  updated_at DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uniq_skel_rel_path (rel_path)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
