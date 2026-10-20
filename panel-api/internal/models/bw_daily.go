package models

import "time"

// BWDaily — per-domain daily bandwidth and request totals harvested
// from goaccess output of nginx access logs (M13.1).
//
// Composite primary key (DomainID, Day) enforces one row per domain
// per UTC date; the agent's scan handler upserts.
type BWDaily struct {
	DomainID      string    `gorm:"column:domain_id;type:varchar(26);primaryKey" json:"domain_id"`
	Day           time.Time `gorm:"column:day;type:date;primaryKey" json:"day"`
	BytesTotal    uint64    `gorm:"column:bytes_total;type:bigint unsigned;not null;default:0" json:"bytes_total"`
	RequestsTotal uint64    `gorm:"column:requests_total;type:bigint unsigned;not null;default:0" json:"requests_total"`
	UpdatedAt     time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (BWDaily) TableName() string { return "bw_daily" }

// BWDailyCountry — one domain's traffic from one country on one day, read
// from the geolocation panel of the same goaccess scan as BWDaily (only on
// hosts with the GeoIP database). CountryCode is ISO 3166-1 alpha-2, or "ZZ"
// for addresses with no country and the countries past the scan's cap.
type BWDailyCountry struct {
	DomainID      string    `gorm:"column:domain_id;type:varchar(26);primaryKey" json:"domain_id"`
	Day           time.Time `gorm:"column:day;type:date;primaryKey" json:"day"`
	CountryCode   string    `gorm:"column:country_code;type:char(2);primaryKey" json:"country_code"`
	BytesTotal    uint64    `gorm:"column:bytes_total;type:bigint unsigned;not null;default:0" json:"bytes_total"`
	RequestsTotal uint64    `gorm:"column:requests_total;type:bigint unsigned;not null;default:0" json:"requests_total"`
	UpdatedAt     time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (BWDailyCountry) TableName() string { return "bw_daily_country" }
