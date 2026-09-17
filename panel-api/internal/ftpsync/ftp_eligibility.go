package ftpsync

import (
	"context"
	"sort"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// PackageGetter is the one repository capability the effective-access
// projection needs: resolve a hosting package by ID. Both
// repository.PackageRepository and the reconciler/api test fakes satisfy it
// structurally, so neither the reconciler nor the API handler grows a new
// dependency to share this code.
type PackageGetter interface {
	FindByID(ctx context.Context, id string) (*models.HostingPackage, error)
}

// Eligibility captures whether a tenant may currently HOLD active FTP/SFTP
// credentials and how many simultaneously. It is the shared projection input
// that BOTH dispatch sites — the reconciler's periodic pass and the API's
// immediate syncHostAccess — must agree on, so a mutation on one tenant can
// never dispatch a wider snapshot than the reconciler would (JAB-276): a wider
// immediate sync carries a higher generation than an earlier revocation and
// would re-emit the revoked account's sshd Match block.
type Eligibility struct {
	Eligible bool // false => lock ALL of this tenant's aliases
	Cap      int  // max simultaneously-enabled accounts (0 when !Eligible)
}

// OwnerEligibility resolves whether a tenant may hold active FTP/SFTP
// credentials right now: NOT administratively suspended (JAB-254) AND the
// hosting package still includes the feature (max_ftp_accounts > 0, JAB-258).
// A nil user, missing package, or lookup failure is treated as ineligible
// (fail closed) — a same-uid credential is privileged surface, so the clamp
// must never fail open. pkgs may be nil (treated as ineligible).
func OwnerEligibility(ctx context.Context, pkgs PackageGetter, u *models.User) Eligibility {
	if u == nil || u.Suspended {
		return Eligibility{}
	}
	if pkgs == nil || u.PackageID == nil || *u.PackageID == "" {
		return Eligibility{}
	}
	pkg, err := pkgs.FindByID(ctx, *u.PackageID)
	if err != nil || pkg == nil || pkg.MaxFTPAccounts == 0 {
		return Eligibility{}
	}
	return Eligibility{Eligible: true, Cap: int(pkg.MaxFTPAccounts)}
}

// EffectiveEnabled applies owner eligibility on top of each row's own
// IsEnabled. accts MUST be the tenant's full set; over-cap accounts
// (oldest-first) beyond Cap are forced disabled deterministically. Returns
// username -> effective-enabled.
func EffectiveEnabled(accts []models.FtpAccount, elig Eligibility) map[string]bool {
	out := make(map[string]bool, len(accts))
	if !elig.Eligible {
		for _, a := range accts {
			out[a.Username] = false
		}
		return out
	}
	// Oldest-first so a cap reduction disables the NEWEST accounts, keeping the
	// tenant's long-standing credentials working.
	ordered := append([]models.FtpAccount(nil), accts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})
	kept := 0
	for _, a := range ordered {
		if !a.IsEnabled {
			out[a.Username] = false
			continue
		}
		if elig.Cap > 0 && kept >= elig.Cap {
			out[a.Username] = false // over-cap: excess disabled
			continue
		}
		out[a.Username] = true
		kept++
	}
	return out
}
