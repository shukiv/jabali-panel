-- Reverse JAB-171 phase 1.
DROP TABLE user_notification_routes;

ALTER TABLE notification_channels DROP FOREIGN KEY fk_notif_channels_user;
ALTER TABLE notification_channels DROP KEY ix_notif_channels_user;
ALTER TABLE notification_channels DROP COLUMN user_id;
