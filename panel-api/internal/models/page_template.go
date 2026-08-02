package models

import "time"

// PageTemplate is an operator-editable stock HTML body keyed by a
// short identifier (domain_default_index, error_404, error_403,
// error_500). Rendered by panel-api (domain create) or nginx error
// pages (future wiring). Seeded on first boot by
// PageTemplateRepository.EnsureDefaults.
type PageTemplate struct {
	Key       string    `gorm:"column:key;primaryKey;type:varchar(64)" json:"key"`
	Content   string    `gorm:"column:content;type:longtext;not null"  json:"content"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (PageTemplate) TableName() string { return "page_templates" }

// PageTemplateKey enum — the set of known templates. Handlers
// validate incoming key against this set so operators can't create
// rows that no consumer reads.
const (
	PageTemplateDomainDefaultIndex = "domain_default_index"
	PageTemplateDomainDisabled     = "domain_disabled"
	PageTemplateDomainUnconfigured = "domain_unconfigured"
	PageTemplateError404           = "error_404"
	PageTemplateError403           = "error_403"
	PageTemplateError500           = "error_500"
)

// AllPageTemplateKeys lists every known key. Ordered for stable UI
// display; handlers iterate this list for EnsureDefaults and for the
// list endpoint.
var AllPageTemplateKeys = []string{
	PageTemplateDomainDefaultIndex,
	PageTemplateDomainDisabled,
	PageTemplateDomainUnconfigured,
	PageTemplateError404,
	PageTemplateError403,
	PageTemplateError500,
}
