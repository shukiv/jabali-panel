package models

import "time"

// WebDomainAlias is an additional hostname served from the same vhost
// and docroot as its owning web domain (GH #1625). The reconciler adds
// every alias to the main server block's server_name; an alias that
// resolves DIRECTLY to this server is also added to the domain's TLS
// certificate as a SAN (reachableSANs gates that — a CDN-fronted alias
// is intentionally left off the cert; see reconciler.sanHostnamesForDomain).
//
// Jabali is truth: the reconciler converges nginx + the cert to the set
// of alias rows on every tick. hostname is globally unique across all
// aliases (ux_web_domain_aliases_hostname); the create handler enforces
// the wider "no collision with any domain name or helper server_name"
// rule the DB index cannot express.
type WebDomainAlias struct {
	ID        string    `gorm:"type:char(26);primaryKey" json:"id"`
	DomainID  string    `gorm:"type:char(26);not null;index:idx_web_domain_aliases_domain" json:"domain_id"`
	Hostname  string    `gorm:"type:varchar(253);not null;uniqueIndex:ux_web_domain_aliases_hostname" json:"hostname"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (WebDomainAlias) TableName() string { return "web_domain_aliases" }
