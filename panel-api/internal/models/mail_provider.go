package models

// Mail provider enum (GH#181 / ADR-0120). MailProvider on a Domain is the
// single source of truth for the domain's mail posture; EmailEnabled and
// SkipAutoSAN are derived from it via DeriveMailFlags so the two booleans
// can never drift from operator intent.
const (
	MailProviderJabali = "jabali" // Jabali is the MTA (default; today's behavior)
	MailProviderNone   = "none"   // no mail anywhere — no mail records, no mail SANs
	MailProviderM365   = "m365"   // Microsoft 365
	MailProviderGoogle = "google" // Google Workspace
	// MailProviderCustom (GH #1627) is the posture of a domain created from a
	// custom DNS template: mail (and other services) live wherever the template's
	// records point. Like m365/google it is external — Jabali is not the MTA and
	// no Jabali mail SANs go on the cert. Unlike them the reconciler asserts
	// NOTHING for it: reconcileMailProviderRecords hands the zone off entirely so
	// the template's own apex MX/SPF/DKIM survive as tenant-owned records. It is
	// NEVER caller-supplied — createDomainOp sets it only when a dns_template_id
	// is chosen, and rejects a request that names it directly.
	MailProviderCustom = "custom"
)

// ValidMailProvider reports whether p is a recognised provider value.
// MailProviderCustom is recognised (so a persisted 'custom' row validates
// everywhere), but createDomainOp rejects it as CALLER input — 'custom' is only
// reachable by selecting a dns_template_id (GH #1627).
func ValidMailProvider(p string) bool {
	switch p {
	case MailProviderJabali, MailProviderNone, MailProviderM365, MailProviderGoogle, MailProviderCustom:
		return true
	default:
		return false
	}
}

// MailProviderIsExternal reports whether the provider hosts mail somewhere
// other than Jabali (M365 / Google) — i.e. Jabali is not the MTA but the
// domain still has mail (so provider DNS records are published).
func MailProviderIsExternal(p string) bool {
	return p == MailProviderM365 || p == MailProviderGoogle || p == MailProviderCustom
}

// DeriveMailFlags returns the (EmailEnabled, SkipAutoSAN) pair implied by a
// provider. This is the ONLY place those two booleans are computed from
// provider intent — callers must use it on create/edit so the columns the
// reconciler + cert paths read stay consistent with the enum.
//
//	jabali                 -> (true,  false)  Jabali MTA + jabali mail SANs on cert
//	none/m365/google/custom-> (false, true)   not the Jabali MTA; no jabali mail SANs
func DeriveMailFlags(provider string) (emailEnabled, skipAutoSAN bool) {
	if provider == MailProviderJabali {
		return true, false
	}
	return false, true
}
