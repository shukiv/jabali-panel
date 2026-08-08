-- GH #331 DR standby, Step 1: server role + one-way pairing foundation.
--
-- server_role: 'primary' (default — every existing install is unchanged and
--   fully active) or 'standby' (a one-way async DR replica that pulls the
--   primary's state and does not serve live traffic until manually promoted).
--   The whole DR feature is inert until an operator runs `jabali dr pair`.
-- dr_paired_at / dr_peer_label: when this box was paired + a human label for the
--   peer it replicates from (primary's hostname, for the admin readout).
-- dr_destination_id: the backup_destination this box uses as the one-way DR
--   channel — the primary SHIPS system backups to it, the standby PULLS from it.
--   NULL on a primary that hasn't configured DR. Read/restore-only for a standby;
--   the standby never holds a primary-mutating credential (least privilege).
ALTER TABLE server_settings
  ADD COLUMN server_role       VARCHAR(16)  NOT NULL DEFAULT 'primary',
  ADD COLUMN dr_paired_at      DATETIME(6)  NULL,
  ADD COLUMN dr_peer_label     VARCHAR(255) NOT NULL DEFAULT '',
  ADD COLUMN dr_destination_id CHAR(26)     NULL;
