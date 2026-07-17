-- GH #456: add 'telegram' to notification_channels.kind ENUM.
-- The Telegram channel POSTs `{chat_id, text}` JSON to the Bot API
-- sendMessage endpoint (https://api.telegram.org/bot<token>/sendMessage);
-- bot_token + chat_id live in the per-channel config_json blob.
ALTER TABLE notification_channels
  MODIFY COLUMN kind ENUM('email','slack','discord','ntfy','webhook','webpush','sms','telegram') NOT NULL;
