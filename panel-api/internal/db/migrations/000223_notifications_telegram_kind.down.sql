-- Revert the ENUM to the pre-#456 set. Fails if any telegram channels
-- still exist — delete them first if rolling back.
ALTER TABLE notification_channels
  MODIFY COLUMN kind ENUM('email','slack','discord','ntfy','webhook','webpush','sms') NOT NULL;
