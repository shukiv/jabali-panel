package models

import "time"

// DomainDirectoryPrivacyRule is one (domain, path) protection rule (M50).
//
// Realm is the WWW-Authenticate "realm" string the browser shows in
// the login dialog. Each rule has N credentials (DomainDirectoryPrivacyCredential).
// The agent writes /etc/jabali-panel/dir-privacy/<rule_id>.htpasswd and
// the vhost emits `location ^~ <path>/ { auth_basic <realm>; auth_basic_user_file <file>; }`.
type DomainDirectoryPrivacyRule struct {
	ID        string    `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	DomainID  string    `gorm:"column:domain_id;type:char(26);not null;index:idx_domain_directory_privacy_domain" json:"domain_id"`
	Path      string    `gorm:"column:path;type:varchar(255);not null;uniqueIndex:uniq_domain_directory_privacy_path,priority:2" json:"path"`
	Realm     string    `gorm:"column:realm;type:varchar(255);not null;default:Restricted" json:"realm"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (DomainDirectoryPrivacyRule) TableName() string { return "domain_directory_privacy" }

// DomainDirectoryPrivacyCredential is one bcrypt-hashed user under a rule.
//
// PasswordHash is a bcrypt string ($2y/$2a/$2b...) — the plaintext is
// never stored. The agent writes `<username>:<password_hash>` lines
// into the rule's htpasswd file; nginx auth_basic supports bcrypt.
type DomainDirectoryPrivacyCredential struct {
	ID           string    `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	RuleID       string    `gorm:"column:rule_id;type:char(26);not null;index:idx_domain_directory_privacy_cred_rule;uniqueIndex:uniq_domain_directory_privacy_cred_user,priority:1" json:"rule_id"`
	Username     string    `gorm:"column:username;type:varchar(64);not null;uniqueIndex:uniq_domain_directory_privacy_cred_user,priority:2" json:"username"`
	PasswordHash string    `gorm:"column:password_hash;type:varchar(120);not null" json:"-"`
	CreatedAt    time.Time `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
}

func (DomainDirectoryPrivacyCredential) TableName() string {
	return "domain_directory_privacy_credentials"
}
