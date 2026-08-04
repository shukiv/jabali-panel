package models

import "time"

// CRSRuleExclusion is an operator-managed CRS false-positive exclusion
// (JAB-227). Host and URIPrefix are both required: the only removal construct
// that works in Coraza (ctl:ruleRemoveById) drops the rule for the whole
// matched request, so an unscoped entry would be a WAF hole rather than a
// false-positive fix. Enforced in appseccfg.ValidateExclusion.
type CRSRuleExclusion struct {
	ID        string    `gorm:"column:id;primaryKey;type:varchar(26)"      json:"id"`
	Host      string    `gorm:"column:host;type:varchar(253);not null"     json:"host"`
	URIPrefix string    `gorm:"column:uri_prefix;type:varchar(512);not null" json:"uri_prefix"`
	RuleID    string    `gorm:"column:rule_id;type:varchar(16);not null"   json:"rule_id"`
	Note      string    `gorm:"column:note;type:varchar(512);not null"     json:"note"`
	CreatedAt time.Time `gorm:"column:created_at"                          json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"                          json:"updated_at"`
}

func (CRSRuleExclusion) TableName() string { return "crs_rule_exclusions" }
