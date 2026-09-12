package models

import "time"

// WebTemplate (GH #1624 / ADR-0169 Phase 3) is an admin-defined, named preset of
// raw nginx directives — the "copy my working config" migration vehicle
// (@johnnyq, #1624). An admin authors the directives; at Web Domain create an
// ADMIN may select a template and its NginxDirectives are snapshot-copied onto
// the new domain's NginxCustomDirectives (Domain.NginxCustomDirectives).
//
// Snapshot, not a live link: later edits to a template do not propagate to
// domains already created from it. The directives are validated at BOTH template
// save AND create-apply with api.ValidateNginxDirectivesAdmin, so a template can
// never carry a directive the admin custom-directives PATCH would reject.
//
// ADMIN-SELECT-ONLY in this phase (see the create-op gate): the admin denylist
// does not block proxy_pass, so a globally-visible template a tenant could pick
// is an SSRF vector with the admin as unwitting author. Global — every admin sees
// every template; per-package / tenant-selectable assignment is a later phase.
type WebTemplate struct {
	ID              string    `gorm:"type:char(26);primaryKey" json:"id"`
	Name            string    `gorm:"type:varchar(120);not null;uniqueIndex:ux_web_templates_name" json:"name"`
	Description     string    `gorm:"type:varchar(500);not null;default:''" json:"description"`
	NginxDirectives string    `gorm:"type:text;not null" json:"nginx_directives"`
	CreatedAt       time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

// TableName pins the table (GORM would pluralise to "web_templates" anyway;
// pinned for clarity + rename safety, mirroring DNSTemplate).
func (WebTemplate) TableName() string { return "web_templates" }
