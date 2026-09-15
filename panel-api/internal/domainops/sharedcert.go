package domainops

import (
	"encoding/json"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Shared-certificate coverage matching (JAB-170 / JAB-279). A domain may be
// served by a pre-uploaded shared wildcard/multi-SAN certificate instead of
// waiting for ACME: the create path picks the first server-wide-or-owned cert
// whose SANs cover the domain name. Three adapters need that decision — the
// REST create handler (domain_create_op.go), the REST attach handler
// (ssl_shared.go), and the operator CLI (`jabali domain create` and
// `jabali ssl shared attach`) — so the SAN-cover predicate lives here as one
// leaf, the same one-policy-per-leaf shape as NormalizeDomainName,
// CheckOwnerEligible, ValidateDocumentRoot, and MailProviderForServer.
//
// Before this leaf the matcher was copied three times (api/ssl_shared.go's
// hostMatchesSAN, cmd/server/ssl_shared_cmd.go's cliHostMatchesSAN, and the two
// coversHost loops around them). A cert-coverage matcher is security-adjacent —
// a wildcard bug decides whether a certificate is allowed to serve a host — so
// the copies must not be allowed to drift; every adapter now routes through
// these functions.
//
// The functions are pure: they take the SANs (a JSON array string, exactly as
// models.SharedCertificate stores it) and the host, and the already-filtered
// candidate list. The DB read that produces the list (ListServerWideAndOwned)
// stays adapter-side, per ADR-0083.

// HostMatchesSAN reports whether a certificate SAN covers host, with
// x509.VerifyHostname semantics: a case-insensitive exact match, or a single
// label matched by a leading-label wildcard (*.example.com matches
// sub.example.com but NOT example.com and NOT a.b.example.com). Both inputs are
// trimmed and lowercased; an empty SAN or host never matches.
func HostMatchesSAN(san, host string) bool {
	san = strings.ToLower(strings.TrimSpace(san))
	host = strings.ToLower(strings.TrimSpace(host))
	if san == "" || host == "" {
		return false
	}
	if san == host {
		return true
	}
	if strings.HasPrefix(san, "*.") {
		base := san[2:]
		if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == base {
			return true
		}
	}
	return false
}

// SharedCertCoversHost reports whether any SAN in sansJSON (the JSON array a
// shared certificate stores in its SANs column) covers host. A nil pointer or
// malformed JSON is treated as "covers nothing" (false) rather than an error:
// the caller is deciding whether a cert may serve a host, and an unreadable SAN
// list must never be read as coverage.
func SharedCertCoversHost(sansJSON *string, host string) bool {
	if sansJSON == nil {
		return false
	}
	var sans []string
	if json.Unmarshal([]byte(*sansJSON), &sans) != nil {
		return false
	}
	for _, sn := range sans {
		if HostMatchesSAN(sn, host) {
			return true
		}
	}
	return false
}

// CoveringSharedCert returns the first certificate in certs whose SANs cover
// host, or nil when none does. Callers pass the already server-wide-or-owned
// filtered list (ListServerWideAndOwned), so this leaf makes only the cover
// decision — it never widens the candidate set. First-wins matches the REST
// create path's existing behaviour; the returned pointer aliases the element in
// certs.
func CoveringSharedCert(certs []models.SharedCertificate, host string) *models.SharedCertificate {
	for i := range certs {
		if SharedCertCoversHost(certs[i].SANs, host) {
			return &certs[i]
		}
	}
	return nil
}
