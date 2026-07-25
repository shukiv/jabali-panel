package models

import "time"

// UserNotificationRoute maps a tenant's chosen event kind to one of their own
// notification channels (JAB-171). A route's presence means "deliver this
// event kind to this channel for this user"; its absence means the user has
// not opted that event into that channel. Server/admin events are unaffected —
// they fan out to server-wide channels (notification_channels.user_id IS NULL).
type UserNotificationRoute struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	UserID    string    `gorm:"column:user_id;type:char(26);not null" json:"user_id"`
	EventKind string    `gorm:"column:event_kind;type:varchar(64);not null" json:"event_kind"`
	ChannelID string    `gorm:"column:channel_id;type:char(26);not null" json:"channel_id"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (UserNotificationRoute) TableName() string { return "user_notification_routes" }
