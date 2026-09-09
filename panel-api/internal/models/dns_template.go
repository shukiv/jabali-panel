package models

import "time"

// DNSTemplate (GH #1627) is an admin-defined, named preset of DNS records a
// tenant may select when creating a Web Domain or DNS Zone. It is the stored
// equivalent of the built-in Jabali / Microsoft 365 / Google Workspace mail
// presets that live in code (dnscompile). Global in this phase — every tenant
// sees every template; per-package assignment is a later phase.
type DNSTemplate struct {
	ID          string    `gorm:"type:char(26);primaryKey" json:"id"`
	Name        string    `gorm:"type:varchar(120);not null;uniqueIndex:ux_dns_templates_name" json:"name"`
	Description string    `gorm:"type:varchar(500);not null;default:''" json:"description"`
	CreatedAt   time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`

	// Records is the ordered record set. It is loaded EXPLICITLY by the
	// repository (gorm:"-", not an association) so the API and the reconciler
	// control the query and its ordering — mirrors how DNSTemplate's records
	// are seeded and read, never lazy-loaded mid-request.
	Records []DNSTemplateRecord `gorm:"-" json:"records"`
}

// TableName pins the table (GORM would otherwise pluralise to "dns_templates",
// which happens to match — pinned explicitly for clarity + rename safety).
func (DNSTemplate) TableName() string { return "dns_templates" }

// DNSTemplateRecord is one record blueprint inside a DNSTemplate. Its shape
// mirrors models.DNSRecord (and is validated by the same api.ValidateDNSRecord)
// minus the zone/managed columns — a template record is a blueprint, not a live
// row. The single supported substitution token is {domain}: it is replaced with
// the zone name in Name and Content when the reconciler seeds the record.
type DNSTemplateRecord struct {
	ID         string    `gorm:"type:char(26);primaryKey" json:"id"`
	TemplateID string    `gorm:"type:char(26);not null;index:idx_dns_template_records_template" json:"template_id"`
	Name       string    `gorm:"type:varchar(255);not null" json:"name"`
	Type       string    `gorm:"type:varchar(16);not null" json:"type"`
	Content    string    `gorm:"type:varchar(4096);not null" json:"content"`
	TTL        int       `gorm:"type:int;not null;default:3600" json:"ttl"`
	Priority   int       `gorm:"type:int;not null;default:0" json:"priority"`
	SortOrder  int       `gorm:"type:int;not null;default:0" json:"sort_order"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DNSTemplateRecord) TableName() string { return "dns_template_records" }
