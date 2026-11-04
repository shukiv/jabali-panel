package models

import "time"

// DNSZone is one authoritative zone served by the panel's PowerDNS.
// Mapped 1:1 to a Domain at creation time; the reconciler keeps
// PowerDNS's native tables in sync with the set of DNSRecords below.
//
// SOA fields live on the zone rather than as a separate record because
// operators almost never want to edit them individually; exposing them
// as scalars lets the API validate each one and saves round-trips.
type DNSZone struct {
	ID       string `gorm:"type:char(26);primaryKey" json:"id"`
	DomainID string `gorm:"type:char(26);not null;uniqueIndex:ux_dns_zones_domain_id" json:"domain_id"`

	// Name is the zone apex (same as domains.name at creation time,
	// denormalised so DNS queries don't have to join).
	Name string `gorm:"type:varchar(255);not null;uniqueIndex:ux_dns_zones_name" json:"name"`

	// Serial is the SOA serial number, bumped by the agent on every
	// push. 0 means "not yet pushed".
	Serial int64 `gorm:"type:bigint;not null;default:0" json:"serial"`

	// SOA timers — operator-tunable later; sane defaults at creation.
	RefreshSeconds int `gorm:"type:int;not null;default:3600"   json:"refresh_seconds"`
	RetrySeconds   int `gorm:"type:int;not null;default:600"    json:"retry_seconds"`
	ExpireSeconds  int `gorm:"type:int;not null;default:604800" json:"expire_seconds"`
	MinimumTTL     int `gorm:"type:int;not null;default:3600"   json:"minimum_ttl"`

	// IsEnabled false keeps the DB row but suppresses PowerDNS push,
	// so the zone goes dark without losing its record set.
	IsEnabled bool `gorm:"type:tinyint(1);not null;default:1" json:"is_enabled"`

	// LastPushedAt is when PowerDNS last accepted this zone from the
	// reconciler; nil means it never has (since the column was added).
	// LastPushError is why the most recent push failed, nil once a push
	// succeeds. Both are written only by DNSZoneRepository.SetPushResult
	// (GH #2377). The error is raw agent output, so it is never put on the
	// wire by default: handlers that may show it (admin scope) copy it out
	// explicitly.
	LastPushedAt  *time.Time `gorm:"type:datetime(6)" json:"last_pushed_at,omitempty"`
	LastPushError *string    `gorm:"type:text" json:"-"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DNSZone) TableName() string { return "dns_zones" }

// PushFailed reports whether the most recent push of the zone to PowerDNS
// failed.
func (z *DNSZone) PushFailed() bool {
	return z.LastPushError != nil && *z.LastPushError != ""
}

// DNSRecord is one resource record inside a DNSZone.
//
// Name uses "@" for the zone apex, short labels ("www", "mail"), or a
// full FQDN ending with the zone name. The agent expands @ to the zone
// apex when writing PowerDNS's records table.
//
// Managed records (SOA, NS, bootstrap A/MX) are panel-owned and shown
// read-only in the UI. Operators can override if they really need to
// by flipping Managed to false from the API.
type DNSRecord struct {
	ID     string `gorm:"type:char(26);primaryKey"                               json:"id"`
	ZoneID string `gorm:"type:char(26);not null;index:ix_dns_records_zone_id"    json:"zone_id"`

	Name     string `gorm:"type:varchar(255);not null"                     json:"name"`
	Type     string `gorm:"type:varchar(16);not null"                      json:"type"`
	Content  string `gorm:"type:varchar(4096);not null"                    json:"content"`
	TTL      int    `gorm:"type:int;not null;default:3600"                 json:"ttl"`
	Priority int    `gorm:"type:int;not null;default:0"                    json:"priority"`

	Managed   bool `gorm:"type:tinyint(1);not null;default:0" json:"managed"`
	// ManagedBy (migration 000055) distinguishes which subsystem owns a
	// managed record. NULL for pre-M6 rows and hand-edits; "m6" for
	// records inserted by domain.email_enable (DKIM + autoconfig CNAME
	// + _autodiscover._tcp SRV). Delete-on-disable scopes cleanup by
	// this column so M4 bootstrap records (MX, SPF, DMARC, A) survive.
	ManagedBy *string `gorm:"type:varchar(16)" json:"managed_by,omitempty"`

	IsEnabled bool `gorm:"type:tinyint(1);not null;default:1" json:"is_enabled"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

func (DNSRecord) TableName() string { return "dns_records" }
