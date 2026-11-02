package models

import (
	"strings"
	"time"
)

// BotChallengeExclusion is a path of a domain that skips the AppSec
// bot-detection challenge (GH #2328). Admin-only, like
// Domain.BotChallengeExempt. The reconciler applies the row to the domain and
// www.<domain>; PathPrefix is matched against the decoded request path
// without the query string (validated by appseccfg.ValidateBotPathPrefix).
//
// Methods is the comma-separated, upper-case, sorted method list, "" for
// every method; MethodList returns it as a list.
type BotChallengeExclusion struct {
	ID         string    `gorm:"column:id;primaryKey;type:char(26)"            json:"id"`
	DomainID   string    `gorm:"column:domain_id;type:char(26);not null"       json:"domain_id"`
	PathPrefix string    `gorm:"column:path_prefix;type:varchar(512);not null" json:"path_prefix"`
	Methods    string    `gorm:"column:methods;type:varchar(64);not null"      json:"-"`
	Note       string    `gorm:"column:note;type:varchar(512);not null"        json:"note"`
	CreatedAt  time.Time `gorm:"column:created_at"                             json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"                             json:"updated_at"`
}

func (BotChallengeExclusion) TableName() string { return "bot_challenge_exclusions" }

// MethodList returns Methods as a list, nil for every method.
func (e BotChallengeExclusion) MethodList() []string {
	if e.Methods == "" {
		return nil
	}
	return strings.Split(e.Methods, ",")
}
