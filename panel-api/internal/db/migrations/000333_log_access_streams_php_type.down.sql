-- Streams live 15 minutes; drop the PHP ones so the narrower ENUM fits.
DELETE FROM log_access_streams WHERE log_type = 'php';

ALTER TABLE log_access_streams
  MODIFY COLUMN log_type ENUM('access', 'error', 'goaccess') NOT NULL;
