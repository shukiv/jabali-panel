-- JAB-171 phase 1: per-user notification channels + routing (schema only).
-- Nothing reads these columns yet — the dispatcher stays global until phase 2.
--
-- notification_channels.user_id: NULL = server-wide (admin-owned, existing
-- behaviour); set = owned by that tenant. Global dispatch will filter
-- `user_id IS NULL` in phase 2.
ALTER TABLE notification_channels
    ADD COLUMN user_id CHAR(26) COLLATE utf8mb4_unicode_ci NULL AFTER id,
    ADD KEY ix_notif_channels_user (user_id),
    ADD CONSTRAINT fk_notif_channels_user FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE CASCADE;

-- user_notification_routes maps a tenant's chosen event kinds to their own
-- channels: (user_id, event_kind) -> channel_id. Absence of a route means the
-- user has not opted that event into that channel. Explicit COLLATE on every
-- FK column so MariaDB's exact-collation FK requirement is met regardless of
-- each referenced table's own collation.
CREATE TABLE user_notification_routes (
    id          CHAR(26)    COLLATE utf8mb4_unicode_ci NOT NULL,
    user_id     CHAR(26)    COLLATE utf8mb4_unicode_ci NOT NULL,
    event_kind  VARCHAR(64) NOT NULL,
    channel_id  CHAR(26)    COLLATE utf8mb4_unicode_ci NOT NULL,
    created_at  DATETIME(6) NOT NULL,
    updated_at  DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_user_event_channel (user_id, event_kind, channel_id),
    KEY ix_user_notif_routes_user (user_id),
    KEY ix_user_notif_routes_channel (channel_id),
    CONSTRAINT fk_user_notif_routes_user FOREIGN KEY (user_id)
        REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_user_notif_routes_channel FOREIGN KEY (channel_id)
        REFERENCES notification_channels (id) ON DELETE CASCADE
)
ENGINE = InnoDB
DEFAULT CHARSET = utf8mb4
COLLATE = utf8mb4_unicode_ci;
