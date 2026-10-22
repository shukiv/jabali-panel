-- The Logs page streams a domain's PHP error log (log_type 'php'), read
-- through the agent from ~/logs/<domain>.php-error.log.
ALTER TABLE log_access_streams
  MODIFY COLUMN log_type ENUM('access', 'error', 'goaccess', 'php') NOT NULL;
