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
// Each row this package creates names its install in the note. That lets a
// sync remove the row once the install is gone, however it went: app delete,
// or a domain or account delete that cascades the install row away. Rows an
// operator added by hand never carry that note and are never touched.

// AppTypeFlarum is the application_installs.app_type of a Flarum install.
const AppTypeFlarum = "flarum"

const (
	flarumRuleID     = "920450"
	flarumNotePrefix = "Flarum X-HTTP-Method-Override (CRS 920450 false positive), managed for Flarum install "
	flarumNoteSuffix = " (GH #1650)"
)

func flarumNote(installID string) string {
	return flarumNotePrefix + installID + flarumNoteSuffix
}

// managedFlarumInstallID returns the install a row was created for, or "" when
// the row is not one this package manages.
func managedFlarumInstallID(r models.CRSRuleExclusion) string {
	if r.RuleID != flarumRuleID || len(r.Note) <= len(flarumNotePrefix)+len(flarumNoteSuffix) ||
		!strings.HasPrefix(r.Note, flarumNotePrefix) || !strings.HasSuffix(r.Note, flarumNoteSuffix) {
		return ""
	}
	id := r.Note[len(flarumNotePrefix) : len(r.Note)-len(flarumNoteSuffix)]
	if strings.ContainsAny(id, " \t") {
		return ""
	}
	return id
}

// FlarumExclusion returns the exclusion a Flarum install needs: CRS 920450
// dropped for the forum's API path, on the host the forum is served from. The
// host and path follow buildSiteURL, which is where Flarum's own base URL comes
// from. The error is ValidateExclusion's; an exclusion that fails it is never
// stored.
func FlarumExclusion(installID, domainName string, useWWW bool, subdirectory string) (appseccfg.Exclusion, error) {
	host := strings.ToLower(strings.TrimSpace(domainName))
	if useWWW {
		host = "www." + host
	}
	prefix := "/api/"
	if sub := strings.Trim(subdirectory, "/"); sub != "" {
		prefix = "/" + sub + "/api/"
	}
	e := appseccfg.Exclusion{Host: host, URIPrefix: prefix, RuleID: flarumRuleID, Note: flarumNote(installID)}
	return e, appseccfg.ValidateExclusion(e)
}

// SyncFlarum brings the Flarum-managed exclusions in line with the installs,
// then applies the result to the host:
//
//   - when installID names a Flarum install, its exclusion is added unless an
//     equal one (same host, path and rule, by any note) already exists;
//   - every managed exclusion whose install no longer exists, or is no longer
//     served at that host and path, is removed.
//
// Pass an empty installID after a delete. On any lookup error nothing more is
// changed and nothing is applied: a row is only removed once its install is
// known to be gone.
func SyncFlarum(ctx context.Context, d Deps, installID string) (appseccfg.OperatorApplyResult, error) {
	mu.Lock()
	defer mu.Unlock()
	if _, err := syncFlarumLocked(ctx, d, installID); err != nil {
		return appseccfg.OperatorApplyResult{}, err
	}
	return applyLocked(ctx, d)
}

// PruneFlarum removes the managed exclusions whose install is gone and applies
// the result only if it removed any. It never adds one. The panel runs it at
// start-up, which catches an install that vanished without a Flarum event (a
// domain or account delete cascades the install row away).
func PruneFlarum(ctx context.Context, d Deps) (changed bool, res appseccfg.OperatorApplyResult, err error) {
	mu.Lock()
	defer mu.Unlock()
	changed, err = syncFlarumLocked(ctx, d, "")
	if err != nil || !changed {
		return changed, res, err
	}
	res, err = applyLocked(ctx, d)
	return changed, res, err
}

func syncFlarumLocked(ctx context.Context, d Deps, installID string) (changed bool, err error) {
	if d.Exclusions == nil || d.Installs == nil || d.Domains == nil {
		return false, ErrNotConfigured
	}
	rows, err := d.Exclusions.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list CRS exclusions: %w", err)
	}

	if installID != "" {
		e, ok, err := expectedFlarumExclusion(ctx, d, installID)
		if err != nil {
			return false, err
		}
		if ok && !hasExclusion(rows, e) {
			row := &models.CRSRuleExclusion{
				ID: ids.NewULID(), Host: e.Host, URIPrefix: e.URIPrefix, RuleID: e.RuleID, Note: e.Note,
			}
			if err := d.Exclusions.Create(ctx, row); err != nil {
				return false, fmt.Errorf("save Flarum exclusion for %s%s: %w", e.Host, e.URIPrefix, err)
			}
			rows = append(rows, *row)
			changed = true
		}
	}

	for _, r := range rows {
		id := managedFlarumInstallID(r)
		if id == "" {
			continue
		}
		e, ok, err := expectedFlarumExclusion(ctx, d, id)
		if err != nil {
			return changed, err
		}
		if ok && strings.EqualFold(r.Host, e.Host) && r.URIPrefix == e.URIPrefix {
			continue
		}
		if err := d.Exclusions.DeleteByID(ctx, r.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return changed, fmt.Errorf("remove stale Flarum exclusion %s: %w", r.ID, err)
		}
		changed = true
	}
	return changed, nil
}

// expectedFlarumExclusion returns the exclusion installID should have. ok is
// false when the install or its domain no longer exists, the install is not
// Flarum, or its exclusion cannot be expressed safely. err is only for a
// failed lookup, where the answer is unknown.
func expectedFlarumExclusion(ctx context.Context, d Deps, installID string) (appseccfg.Exclusion, bool, error) {
	inst, err := d.Installs.FindByID(ctx, installID)
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && inst == nil):
		return appseccfg.Exclusion{}, false, nil
	case err != nil:
		return appseccfg.Exclusion{}, false, fmt.Errorf("look up install %s: %w", installID, err)
	}
	if inst.AppType != AppTypeFlarum {
		return appseccfg.Exclusion{}, false, nil
	}
	dom, err := d.Domains.FindByID(ctx, inst.DomainID)
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && dom == nil):
		return appseccfg.Exclusion{}, false, nil
	case err != nil:
		return appseccfg.Exclusion{}, false, fmt.Errorf("look up domain %s: %w", inst.DomainID, err)
	}
	e, verr := FlarumExclusion(inst.ID, dom.Name, inst.UseWWW, inst.Subdirectory)
	if verr != nil {
		return appseccfg.Exclusion{}, false, nil
	}
	return e, true, nil
}

func hasExclusion(rows []models.CRSRuleExclusion, e appseccfg.Exclusion) bool {
	for _, r := range rows {
		if strings.EqualFold(r.Host, e.Host) && r.URIPrefix == e.URIPrefix && r.RuleID == e.RuleID {
			return true
		}
	}
	return false
}
