package domainops

import "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"

// Mail-provider coercion for the server's mail posture (JAB-279, GH #1409). The
// REST create handler, the automation account-create handler, and the operator
// CLI all route the resolved mail provider through this leaf before deriving the
// mail flags, so a domain never persists Jabali mail on a server whose mail
// module is switched off — the same one-policy-per-leaf shape as
// NormalizeDomainName and CheckOwnerEligible.
//
// Only the Jabali provider is coerced: it is the sole provider whose mail is
// served BY this box, so it is the only one that cannot run when the module is
// off. An external provider (m365 / google / custom) keeps its posture — its
// mail lives elsewhere and needs no local module — and an already-none provider
// stays none.
//
// This is a pure transform, not a gate: the caller reads ServerSettings and
// passes the resulting bool. The read stays adapter-side (DB-touching, per
// ADR-0083), and callers fail OPEN on an unreadable settings row (assume the
// module is installed) so a transient settings failure never blocks a create.
func MailProviderForServer(provider string, mailModuleEnabled bool) string {
	if provider == models.MailProviderJabali && !mailModuleEnabled {
		return models.MailProviderNone
	}
	return provider
}
