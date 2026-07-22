// external_mail_records.go — DNS records for domains whose mail lives at an
// external provider (Microsoft 365 / Google Workspace) or nowhere.
// GH#181 / ADR-0120.
//
// Split into two scopes so a provider switch prunes cleanly:
//   - apex MX / SPF / _dmarc        -> managed_by = ApexMailManagedBy ("mail-apex")
//   - non-apex (autodiscover, DKIM) -> managed_by = "mail-provider-<p>"
//
// Record content is provider CONSTANTS; only the optional DKIM tokens carry
// operator data, and they are validated by the caller before reaching here.
package dnscompile

import (
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ApexMailManagedBy scopes the per-provider apex MX/SPF/_dmarc rows. The
// reconciler owns exactly one of each under this marker so a provider
// switch is delete-by-scope + insert — there can never be two `v=spf1`
// TXT at the apex (RFC 7208 permerror).
const ApexMailManagedBy = "mail-apex"

// ProviderMailManagedBy returns the managed_by marker for a provider's
// NON-apex rows (autodiscover CNAME, DKIM). Empty for jabali/none.
func ProviderMailManagedBy(provider string) string {
	switch provider {
	case models.MailProviderM365:
		return "mail-provider-m365"
	case models.MailProviderGoogle:
		return "mail-provider-google"
	default:
		return ""
	}
}

// dashedDomain turns "example.com" into "example-com" (the M365 MX host
// label form, e.g. example-com.mail.protection.outlook.com).
func dashedDomain(zone string) string {
	return strings.ReplaceAll(zone, ".", "-")
}

// BuildApexMailRecords returns the apex MX + SPF + _dmarc for a provider.
// `none` returns nil (the domain has no mail at the apex). All rows carry
// managed_by = ApexMailManagedBy.
func BuildApexMailRecords(provider, zoneName string, srv *models.ServerSettings, idNew func() string, now time.Time) []models.DNSRecord {
	marker := ApexMailManagedBy
	mk := func(name, typ, content string, prio int) models.DNSRecord {
		return models.DNSRecord{
			ID: idNew(), ZoneID: "", Name: name, Type: typ, Content: content,
			TTL: models.EffectiveDNSTTL(srv), Priority: prio, Managed: true, ManagedBy: &marker,
			IsEnabled: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	switch provider {
	case models.MailProviderJabali:
		return []models.DNSRecord{
			mk("@", "MX", "mail."+zoneName, 10),
			mk("@", "TXT", BuildSPFString(srv), 0),
			mk("_dmarc", "TXT", `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`, 0),
		}
	case models.MailProviderM365:
		return []models.DNSRecord{
			mk("@", "MX", dashedDomain(zoneName)+".mail.protection.outlook.com", 0),
			mk("@", "TXT", `"v=spf1 include:spf.protection.outlook.com -all"`, 0),
			mk("_dmarc", "TXT", `"v=DMARC1; p=none; rua=mailto:postmaster@`+zoneName+`"`, 0),
		}
	case models.MailProviderGoogle:
		return []models.DNSRecord{
			mk("@", "MX", "smtp.google.com", 1),
			mk("@", "TXT", `"v=spf1 include:_spf.google.com ~all"`, 0),
			mk("_dmarc", "TXT", `"v=DMARC1; p=none; rua=mailto:postmaster@`+zoneName+`"`, 0),
		}
	default: // none
		return nil
	}
}

// BuildProviderMailRecords returns the NON-apex provider records
// (autodiscover CNAME + optional DKIM). jabali/none return nil — jabali's
// non-apex set is BuildEmailRecords; none has nothing. ZoneID is left empty
// for the caller to stamp. Tokens must already be validated.
func BuildProviderMailRecords(provider, zoneName, m365Onmicrosoft, googleDKIM string, srv *models.ServerSettings, idNew func() string, now time.Time) []models.DNSRecord {
	marker := ProviderMailManagedBy(provider)
	if marker == "" {
		return nil
	}
	mk := func(name, typ, content string, prio int) models.DNSRecord {
		return models.DNSRecord{
			ID: idNew(), ZoneID: "", Name: name, Type: typ, Content: content,
			TTL: models.EffectiveDNSTTL(srv), Priority: prio, Managed: true, ManagedBy: &marker,
			IsEnabled: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	switch provider {
	case models.MailProviderM365:
		out := []models.DNSRecord{
			mk("autodiscover", "CNAME", "autodiscover.outlook.com", 0),
		}
		if m365Onmicrosoft != "" {
			dashed := dashedDomain(zoneName)
			out = append(out,
				mk("selector1._domainkey", "CNAME", "selector1-"+dashed+"._domainkey."+m365Onmicrosoft, 0),
				mk("selector2._domainkey", "CNAME", "selector2-"+dashed+"._domainkey."+m365Onmicrosoft, 0),
			)
		}
		return out
	case models.MailProviderGoogle:
		var out []models.DNSRecord
		if googleDKIM != "" {
			out = append(out, mk("google._domainkey", "TXT", `"`+googleDKIM+`"`, 0))
		}
		return out
	default:
		return nil
	}
}
