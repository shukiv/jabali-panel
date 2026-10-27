package models

import "time"

// DomainGitWebhook is a domain's deploy-on-push webhook (GH #2277,
// ADR-0178). The Git host posts each push to /api/v1/webhooks/git/<HookID>,
// signed with the shared secret; a push to the domain's branch deploys it.
// The row goes with the domain's Git settings.
type DomainGitWebhook struct {
	DomainID string `gorm:"column:domain_id;type:char(26);primaryKey" json:"-"`
	// HookID is the random key in the webhook's URL. It finds the row and
	// proves nothing: a delivery is believed only by its signature.
	HookID string `gorm:"column:hook_id;type:char(32);not null;uniqueIndex:uq_domain_git_webhooks_hook" json:"-"`
	// SecretEnc is the shared secret, sealed with the sso key.
	SecretEnc []byte `gorm:"column:secret_enc;type:varbinary(1024);not null" json:"-"`
	// QueuedAt: a push arrived while a deployment was running, and one more
	// deployment starts once it has ended.
	QueuedAt *time.Time `gorm:"column:queued_at;type:datetime(6)" json:"-"`
	// LastDeliveryAt and LastResult describe the last delivery with a valid
	// signature: what the panel did with it.
	LastDeliveryAt *time.Time `gorm:"column:last_delivery_at;type:datetime(6)" json:"-"`
	LastResult     string     `gorm:"column:last_result;type:varchar(64);not null" json:"-"`
	CreatedAt      time.Time  `gorm:"type:datetime(6);not null" json:"-"`
	UpdatedAt      time.Time  `gorm:"type:datetime(6);not null" json:"-"`
}

func (DomainGitWebhook) TableName() string { return "domain_git_webhooks" }

// What a webhook delivery led to (DomainGitWebhook.LastResult). A refused
// delivery records "refused:" and the API's error code.
const (
	GitWebhookStarted   = "started"
	GitWebhookQueued    = "queued"
	GitWebhookIgnored   = "ignored"
	GitWebhookDuplicate = "duplicate"
	GitWebhookFailed    = "failed"
	GitWebhookRefused   = "refused:"
)
