package models

import "time"

// DomainWebmailBranding is a domain's own webmail branding (GH #2502): what
// Bulwark shows on the domain's web address, mail.<domain>, instead of the
// server-wide branding. An empty field falls back to the server-wide value.
//
// LogoLight, LogoDark and Favicon are each the short hash of the PNG
// panel-api stored for that picture, "" when there is none. The hash goes
// into the picture's URL, so a browser fetches a changed picture at once.
//
// SetByAdmin: an administrator saved it last. An administrator's branding
// applies whatever the owner's package allows; the owner's own only while
// their package has WebmailBrandingEnabled.
type DomainWebmailBranding struct {
	DomainID    string    `gorm:"column:domain_id;type:char(26);primaryKey" json:"domain_id"`
	AppName     string    `gorm:"column:app_name;type:varchar(64);not null" json:"app_name"`
	CompanyName string    `gorm:"column:company_name;type:varchar(128);not null" json:"company_name"`
	WebsiteURL  string    `gorm:"column:website_url;type:varchar(512);not null" json:"website_url"`
	PrivacyURL  string    `gorm:"column:privacy_url;type:varchar(512);not null" json:"privacy_url"`
	ImprintURL  string    `gorm:"column:imprint_url;type:varchar(512);not null" json:"imprint_url"`
	LogoLight   string    `gorm:"column:logo_light;type:varchar(16);not null" json:"logo_light"`
	LogoDark    string    `gorm:"column:logo_dark;type:varchar(16);not null" json:"logo_dark"`
	Favicon     string    `gorm:"column:favicon;type:varchar(16);not null" json:"favicon"`
	SetByAdmin  bool      `gorm:"column:set_by_admin;type:tinyint(1);not null" json:"set_by_admin"`
	CreatedAt   time.Time `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (DomainWebmailBranding) TableName() string { return "domain_webmail_branding" }

// Empty reports whether the branding sets nothing: every field falls back to
// the server-wide branding.
func (b *DomainWebmailBranding) Empty() bool {
	return b.AppName == "" && b.CompanyName == "" && b.WebsiteURL == "" &&
		b.PrivacyURL == "" && b.ImprintURL == "" &&
		b.LogoLight == "" && b.LogoDark == "" && b.Favicon == ""
}
