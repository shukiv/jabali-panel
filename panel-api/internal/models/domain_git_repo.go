package models

import "time"

// DomainGitRepo is the Git repository a domain deploys from (GH #2277,
// ADR-0178). TokenEnc is the access token sealed with the sso key; it never
// leaves the panel except to the agent for one git call, and no API
// response, log line or backup carries it.
type DomainGitRepo struct {
	DomainID string `gorm:"column:domain_id;type:char(26);primaryKey" json:"domain_id"`
	UserID   string `gorm:"column:user_id;type:char(26);not null" json:"-"`
	// RepoURL is an https:// URL with no credentials in it.
	RepoURL string `gorm:"column:repo_url;type:varchar(512);not null" json:"repo_url"`
	Branch  string `gorm:"type:varchar(255);not null" json:"branch"`
	// Username goes with the token; "" sends gitdeploy.DefaultUsername.
	Username string `gorm:"type:varchar(128);not null" json:"username"`
	// TokenEnc is nil for a public repository. A token is bound to RepoURL's
	// host: saving another host needs the token again.
	TokenEnc []byte `gorm:"column:token_enc;type:varbinary(1024)" json:"-"`
	// VerifiedAt is when the panel last read the repository's branches with
	// these settings.
	VerifiedAt time.Time `gorm:"column:verified_at;type:datetime(6);not null" json:"verified_at"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DomainGitRepo) TableName() string { return "domain_git_repos" }
