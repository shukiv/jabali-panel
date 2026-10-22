package models

import "time"

// LogAccessStream represents a temporary access key for streaming log data.
// Used to provide secure, time-limited access to nginx access logs, error logs,
// and GoAccess real-time reports via WebSocket connections.
type LogAccessStream struct {
	ID        string  `gorm:"type:char(26);primaryKey" json:"id"`
	UserID    string  `gorm:"type:char(26);not null;index:ix_log_access_streams_user_id" json:"user_id"`
	DomainID  *string `gorm:"type:char(26);index:ix_log_access_streams_domain_id" json:"domain_id,omitempty"`

	// LogType specifies which type of log this stream provides access to.
	// Values: "access" (nginx access log), "error" (nginx error log), "goaccess" (GoAccess real-time HTML report),
	// "php" (the domain's PHP error log, read through the agent)
	LogType string `gorm:"type:enum('access','error','goaccess','php');not null" json:"log_type"`

	// StreamKey is a cryptographically random token used to authenticate
	// WebSocket connections and prevent unauthorized access to logs.
	StreamKey string `gorm:"type:char(32);uniqueIndex:ux_log_access_streams_stream_key;not null" json:"stream_key"`

	// ExpiresAt is when this stream key becomes invalid. Used to implement
	// time-limited access and automatic cleanup of stale streams.
	ExpiresAt time.Time `gorm:"type:timestamp;not null;index:ix_log_access_streams_expires_at" json:"expires_at"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (LogAccessStream) TableName() string { return "log_access_streams" }