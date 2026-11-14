UPDATE shared_resource_grants SET rights = 'read' WHERE rights = 'readdelete';
ALTER TABLE shared_resource_grants
  MODIFY rights ENUM('read','readwrite','admin') NOT NULL DEFAULT 'read';
DROP TABLE scan_drops;
