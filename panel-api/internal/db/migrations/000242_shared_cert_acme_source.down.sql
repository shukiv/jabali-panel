ALTER TABLE shared_certificates
  DROP COLUMN source,
  DROP COLUMN last_error,
  DROP COLUMN staging,
  DROP COLUMN last_attempt_at;
