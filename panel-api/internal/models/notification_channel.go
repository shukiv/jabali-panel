package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// NotificationChannelKind enumerates the channel transports. Mirrors the
// ENUM in migration 000064.
const (
	NotificationChannelKindEmail    = "email"
	NotificationChannelKindSlack    = "slack"
	NotificationChannelKindDiscord  = "discord"
	NotificationChannelKindNtfy     = "ntfy"
	NotificationChannelKindWebhook  = "webhook"
	NotificationChannelKindWebpush  = "webpush"
	NotificationChannelKindSMS      = "sms"
	NotificationChannelKindTelegram = "telegram"
)

// NotificationChannel is a configured delivery target for system events.
// See ADR-0056 + plans/m14-notifications.md.
//
// Config is a per-kind blob (see NotificationChannelConfig). The API
// layer validates per-kind before persistence; the DB only enforces
// well-formed JSON.
type NotificationChannel struct {
	ID string `gorm:"type:char(26);primaryKey" json:"id"`
	// UserID scopes ownership (JAB-171): NULL = server-wide (admin-owned,
	// existing behaviour); set = owned by that tenant. The dispatcher filters
	// on this from phase 2 onward.
	UserID *string                   `gorm:"column:user_id;type:char(26)" json:"user_id,omitempty"`
	Name   string                    `gorm:"type:varchar(120);not null" json:"name"`
	Kind   string                    `gorm:"type:varchar(16);not null;index:idx_notification_channels_kind_enabled,priority:1" json:"kind"`
	Config NotificationChannelConfig `gorm:"column:config_json;type:json;not null" json:"config"`
	// No gorm default:1 — a channel can be created --disabled; default:1 would
	// bind true for a false struct value on INSERT (feedback_gorm_default1_bool_zero_value).
	Enabled   bool      `gorm:"type:tinyint(1);not null;index:idx_notification_channels_kind_enabled,priority:2" json:"enabled"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null;default:CURRENT_TIMESTAMP(6)" json:"updated_at"`
}

func (NotificationChannel) TableName() string { return "notification_channels" }

// NotificationChannelConfig holds the per-kind config blob. Fields are
// optional and interpreted per channel kind — see each sender in
// panel-api/internal/notif/senders/ (added in Step 3).
type NotificationChannelConfig struct {
	URL        string   `json:"url,omitempty"`
	Bearer     string   `json:"bearer,omitempty"`
	HMACSecret string   `json:"hmac_secret,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	ToEmail    string   `json:"to_email,omitempty"`
	FromEmail  string   `json:"from_email,omitempty"`

	// SMTPMode selects the email transport: "local" (the default — relay
	// through the loopback Stalwart submission port) or "smtp" (relay
	// through an external SMTP server defined by the SMTP* fields below).
	// Empty string is treated as "local" for backwards compatibility with
	// rows written before this field existed.
	SMTPMode     string `json:"smtp_mode,omitempty"`
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUsername string `json:"smtp_username,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	// SMTPTLS controls TLS handling for the external SMTP transport.
	// Allowed values: "starttls" (the default, RFC 3207, port 587),
	// "tls" (implicit TLS, RFC 8314, port 465), "none" (plaintext,
	// only useful for local fixtures and 25/tcp legacy relays).
	SMTPTLS string `json:"smtp_tls,omitempty"`

	// SMS-channel destination phone number in E.164 format
	// (e.g. "+15551234567"). Sent as the "to" field in the JSON
	// payload POSTed to the gateway URL.
	ToNumber string `json:"to_number,omitempty"`

	// Telegram channel: bot token (from @BotFather) + the chat/channel ID
	// to deliver to. The sender POSTs to the Bot API sendMessage endpoint.
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

func (c *NotificationChannelConfig) Scan(src any) error {
	if src == nil {
		*c = NotificationChannelConfig{}
		return nil
	}
	switch v := src.(type) {
	case []byte:
		return json.Unmarshal(v, c)
	case string:
		return json.Unmarshal([]byte(v), c)
	default:
		return errors.New("notification channel config: unexpected scan source type")
	}
}

func (c NotificationChannelConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}
