package appsecops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// managed.go — the CRS exclusions an app install carries, kept in step with
// the installs.
//
// Some apps make requests a CRS rule blocks even though the app authorises
// each one itself: Flarum's method-override header (GH #1650), Nextcloud's
// WebDAV methods (Discussion #1032). An install of such an app gets narrow
// exclusions: one CRS rule, on the install's own host, on the app's own paths.
// None of them is a platform-wide rule.
//
// Each row this package creates names its install in the note. That lets a
// sync remove the row once the install is gone, however it went: app delete,
// or a domain or account delete that cascades the install row away. Rows an
// operator added by hand never carry that note and are never touched.

// managedApp describes the exclusions one app's installs carry.
type managedApp struct {
	appType string
	ruleID  string
	// notePrefix and noteSuffix wrap the install ID in the note of every row
	// this package creates for the app.
	notePrefix, noteSuffix string
	// exclusions returns what an install needs, served from domainName (with
	// a www. prefix when useWWW) under subdirectory. The error is
	// ValidateExclusion's; an install whose exclusions fail it gets none.
	exclusions func(installID, domainName string, useWWW bool, subdirectory string) ([]appseccfg.Exclusion, error)
}

// managedApps lists every app whose installs carry managed exclusions.
var managedApps = []managedApp{flarumApp, nextcloudApp}

// ManagesApp reports whether installs of appType carry managed exclusions,
// so a caller knows an install or delete of it needs a sync.
func ManagesApp(appType string) bool {
	_, ok := managedAppFor(appType)
	return ok
}

func managedAppFor(appType string) (managedApp, bool) {
	for _, a := range managedApps {
		if a.appType == appType {
			return a, true
		}
	}
	return managedApp{}, false
}

// installID returns the install a row was created for, or "" when the row is
// not one this package manages for the app.
func (a managedApp) installID(r models.CRSRuleExclusion) string {
	if r.RuleID != a.ruleID || len(r.Note) <= len(a.notePrefix)+len(a.noteSuffix) ||
		!strings.HasPrefix(r.Note, a.notePrefix) || !strings.HasSuffix(r.Note, a.noteSuffix) {
		return ""
	}
	id := r.Note[len(a.notePrefix) : len(r.Note)-len(a.noteSuffix)]
	if strings.ContainsAny(id, " \t") {
		return ""
	}
	return id
}

// managedRowInstall returns the install a row was created for by any managed
// app, or "" for a row an operator added.
func managedRowInstall(r models.CRSRuleExclusion) string {
	for _, a := range managedApps {
		if id := a.installID(r); id != "" {
			return id
		}
	}
	return ""
}

// installHost is the host an install is served from: the domain, or its www.
// name when the install uses www. Same as buildSiteURL.
func installHost(domainName string, useWWW bool) string {
	host := strings.ToLower(strings.TrimSpace(domainName))
	if useWWW {
		host = "www." + host
	}
	return host
}

// installPathPrefix is the URI prefix of an app path under the install's
// subdirectory: "/api/" at the docroot, "/forum/api/" under "forum".
func installPathPrefix(subdirectory, path string) string {
	if sub := strings.Trim(subdirectory, "/"); sub != "" {
		return "/" + sub + path
	}
	return path
}

// SyncInstall brings the managed exclusions in line with the installs, then
// applies the result to the host:
//
//   - when installID names an install of a managed app, its exclusions are
//     added unless an equal one (same host, path and rule, by any note)
//     already exists;
//   - every managed exclusion whose install no longer exists, or is no longer
//     served at that host and path, is removed.
//
// Pass an empty installID after a delete. On any lookup error nothing more is
// changed and nothing is applied: a row is only removed once its install is
// known to be gone.
func SyncInstall(ctx context.Context, d Deps, installID string) (appseccfg.OperatorApplyResult, error) {
	var ids []string
	if installID != "" {
		ids = []string{installID}
	}
	mu.Lock()
	defer mu.Unlock()
	if _, _, err := syncManagedLocked(ctx, d, ids); err != nil {
		return appseccfg.OperatorApplyResult{}, err
	}
	return applyLocked(ctx, d)
}

// SyncInstalls is SyncInstall for many installs at once, with a single apply
// (one crowdsec reload). It backs `jabali appsec flarum-sync`, the operator's
// opt-in for forums installed before GH #1650. It returns the exclusions it
// added; an install already covered adds nothing.
func SyncInstalls(ctx context.Context, d Deps, installIDs []string) ([]appseccfg.Exclusion, appseccfg.OperatorApplyResult, error) {
	mu.Lock()
	defer mu.Unlock()
	added, _, err := syncManagedLocked(ctx, d, installIDs)
	if err != nil {
		return added, appseccfg.OperatorApplyResult{}, err
	}
	res, err := applyLocked(ctx, d)
	return added, res, err
}

// Prune removes the managed exclusions whose install is gone and applies the
// result only if it removed any. It never adds one. The panel runs it at
// start-up, which catches an install that vanished without an app event (a
// domain or account delete cascades the install row away).
func Prune(ctx context.Context, d Deps) (changed bool, res appseccfg.OperatorApplyResult, err error) {
	mu.Lock()
	defer mu.Unlock()
	_, changed, err = syncManagedLocked(ctx, d, nil)
	if err != nil || !changed {
		return changed, res, err
	}
	res, err = applyLocked(ctx, d)
	return changed, res, err
}

// syncManagedLocked registers the exclusions of each install in installIDs,
// then removes every stale managed row. It reports what it added and whether
// any row changed. mu must be held.
func syncManagedLocked(ctx context.Context, d Deps, installIDs []string) (added []appseccfg.Exclusion, changed bool, err error) {
	if d.Exclusions == nil || d.Installs == nil || d.Domains == nil {
		return nil, false, ErrNotConfigured
	}
	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("list CRS exclusions: %w", err)
	}

	for _, installID := range installIDs {
		want, err := expectedExclusions(ctx, d, installID)
		if err != nil {
			return added, changed, err
		}
		for _, e := range want {
			if hasExclusion(rows, e) {
				continue
			}
			row := &models.CRSRuleExclusion{
				ID: ids.NewULID(), Host: e.Host, URIPrefix: e.URIPrefix, RuleID: e.RuleID, Note: e.Note,
			}
			if err := d.Exclusions.Create(ctx, row); err != nil {
				return added, changed, fmt.Errorf("save app exclusion for %s%s: %w", e.Host, e.URIPrefix, err)
			}
			rows = append(rows, *row)
			added = append(added, e)
			changed = true
		}
	}

	for _, r := range rows {
		id := managedRowInstall(r)
		if id == "" {
			continue
		}
		want, err := expectedExclusions(ctx, d, id)
		if err != nil {
			return added, changed, err
		}
		if hasExclusion([]models.CRSRuleExclusion{r}, want...) {
			continue
		}
		if err := d.Exclusions.DeleteByID(ctx, r.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return added, changed, fmt.Errorf("remove stale app exclusion %s: %w", r.ID, err)
		}
		changed = true
	}
	return added, changed, nil
}

// expectedExclusions returns the exclusions installID should have: none when
// the install or its domain no longer exists, the install's app carries no
// managed exclusions, or its exclusions cannot be expressed safely. err is
// only for a failed lookup, where the answer is unknown.
func expectedExclusions(ctx context.Context, d Deps, installID string) ([]appseccfg.Exclusion, error) {
	inst, err := d.Installs.FindByID(ctx, installID)
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && inst == nil):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("look up install %s: %w", installID, err)
	}
	app, ok := managedAppFor(inst.AppType)
	if !ok {
		return nil, nil
	}
	dom, err := d.Domains.FindByID(ctx, inst.DomainID)
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && dom == nil):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("look up domain %s: %w", inst.DomainID, err)
	}
	xs, verr := app.exclusions(inst.ID, dom.Name, inst.UseWWW, inst.Subdirectory)
	if verr != nil {
		return nil, nil
	}
	return xs, nil
}

// hasExclusion reports whether any row has the host, path and rule of any of
// the exclusions.
func hasExclusion(rows []models.CRSRuleExclusion, es ...appseccfg.Exclusion) bool {
	for _, r := range rows {
		for _, e := range es {
			if strings.EqualFold(r.Host, e.Host) && r.URIPrefix == e.URIPrefix && r.RuleID == e.RuleID {
				return true
			}
		}
	}
	return false
}
