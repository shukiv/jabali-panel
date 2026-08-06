ALTER TABLE server_settings
  DROP COLUMN server_role,
  DROP COLUMN dr_paired_at,
  DROP COLUMN dr_peer_label,
  DROP COLUMN dr_destination_id;
