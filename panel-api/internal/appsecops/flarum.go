package appsecops

import (
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// flarum.go — GH #1650. A Flarum install gets one scoped CRS exclusion.
//
// Flarum's JS client sends PATCH and DELETE as a POST carrying an
// X-HTTP-Method-Override header. That header is on CRS's restricted list, so
// rule 920450 blocks every post edit and every mark-as-read. Flarum is safe to
// exempt because it authorises each operation server-side, however the verb
// arrived.
//
// The exemption is deliberately narrow: rule 920450 only, on the forum's own
// host and API path only. It is NOT a platform-wide rule. On Jabali, CRS 911100
// blocks PATCH/PUT/DELETE on ordinary vhosts, and 920450 is what stops
// POST + X-HTTP-Method-Override from walking around that for any other
// framework that honours the header.
//
// managed.go keeps the rows in step with the installs.

// AppTypeFlarum is the application_installs.app_type of a Flarum install.
const AppTypeFlarum = "flarum"

const (
	flarumRuleID = "920450"
	// The note format is stored in existing rows: changing it would orphan
	// them from the sync.
	flarumNotePrefix = "Flarum X-HTTP-Method-Override (CRS 920450 false positive), managed for Flarum install "
	flarumNoteSuffix = " (GH #1650)"
)

var flarumApp = managedApp{
	appType:    AppTypeFlarum,
	ruleID:     flarumRuleID,
	notePrefix: flarumNotePrefix,
	noteSuffix: flarumNoteSuffix,
	exclusions: func(installID, domainName string, useWWW bool, subdirectory string) ([]appseccfg.Exclusion, error) {
		e, err := FlarumExclusion(installID, domainName, useWWW, subdirectory)
		if err != nil {
			return nil, err
		}
		return []appseccfg.Exclusion{e}, nil
	},
}

// FlarumExclusion returns the exclusion a Flarum install needs: CRS 920450
// dropped for the forum's API path, on the host the forum is served from. The
// host and path follow buildSiteURL, which is where Flarum's own base URL comes
// from. The error is ValidateExclusion's; an exclusion that fails it is never
// stored.
func FlarumExclusion(installID, domainName string, useWWW bool, subdirectory string) (appseccfg.Exclusion, error) {
	e := appseccfg.Exclusion{
		Host:      installHost(domainName, useWWW),
		URIPrefix: installPathPrefix(subdirectory, "/api/"),
		RuleID:    flarumRuleID,
		Note:      flarumNotePrefix + installID + flarumNoteSuffix,
	}
	return e, appseccfg.ValidateExclusion(e)
}
