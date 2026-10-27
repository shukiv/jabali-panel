package models

import "time"

// DomainComposerSettings is the Composer project folder a domain uses (GH
// #2276, ADR-0176). A domain with no row finds composer.json from its
// document root up to its own folder.
type DomainComposerSettings struct {
	DomainID string `gorm:"column:domain_id;type:char(26);primaryKey" json:"-"`
	// ProjectDir is relative to the user's home: "domains" or a folder under
	// it. Several domains of one user may share it, e.g. one composer.json and
	// vendor/ in ~/domains/.
	ProjectDir string    `gorm:"column:project_dir;type:varchar(255);not null" json:"project_dir"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DomainComposerSettings) TableName() string { return "domain_composer_settings" }
