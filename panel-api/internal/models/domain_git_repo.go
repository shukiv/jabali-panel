package models

import (
	"strings"
	"time"
)

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
	// WebDir is the directory the site serves, relative to the repository;
	// "" is its root.
	WebDir string `gorm:"column:web_dir;type:varchar(255);not null" json:"web_dir"`
	// SharedPaths holds the paths kept across releases in
	// domains/<domain>/shared, one per line; see SharedPathList.
	SharedPaths string `gorm:"column:shared_paths;type:text" json:"-"`
	// DeployCommands holds what a deployment runs in the new release before
	// it goes live, one command per line (cronvalidate.ValidateDeployCommands).
	DeployCommands string `gorm:"column:deploy_commands;type:text" json:"deploy_commands"`
	// VerifiedAt is when the panel last read the repository's branches with
	// these settings.
	VerifiedAt time.Time `gorm:"column:verified_at;type:datetime(6);not null" json:"verified_at"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DomainGitRepo) TableName() string { return "domain_git_repos" }

// SharedPathList is the repository's shared paths.
func (r DomainGitRepo) SharedPathList() []string {
	if r.SharedPaths == "" {
		return []string{}
	}
	return strings.Split(r.SharedPaths, "\n")
}

// SetSharedPathList stores the shared paths. A shared path never holds a
// newline: the API accepts only cronvalidate.ValidateDeployLayout paths.
func (r *DomainGitRepo) SetSharedPathList(paths []string) {
	r.SharedPaths = strings.Join(paths, "\n")
}
