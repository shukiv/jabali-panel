package backupmetadata

import (
	"context"
	"fmt"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// restoreDomainAliases adds the backup's web domain aliases (GH #1625) of dm
// to dom, the account's domain that stands for it here (GH #1993). An alias
// dom already has stays as it is; the restore never removes one. Each other
// alias goes through the alias page's checks against dom (CheckAlias) and
// gets a new id. Its ownership follows the domain rule: an admin-run restore
// vouches for it, unless the backup records it as pending.
func restoreDomainAliases(ctx context.Context, d Deps, r *ApplyResult, dm internalbackup.MetadataDomain, dom *models.Domain, now time.Time) {
	report := func(format string, args ...any) {
		r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): ", dm.ID, dm.Name)+fmt.Sprintf(format, args...))
	}
	switch {
	case d.CheckAlias == nil:
		report("%d aliases not restored: the alias checks are not wired", len(dm.Aliases))
		return
	case d.WebDomainAliases == nil:
		report("%d aliases not restored: the alias store is not wired", len(dm.Aliases))
		return
	}
	existing, err := d.WebDomainAliases.ListByDomain(ctx, dom.ID)
	if err != nil {
		report("%d aliases not restored: list the domain's aliases: %v", len(dm.Aliases), err)
		return
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[aliasKey(a.Hostname)] = true
	}
	added := 0
	for _, a := range dm.Aliases {
		if have[aliasKey(a.Hostname)] {
			continue
		}
		host, err := d.CheckAlias(ctx, dom, a.Hostname)
		if err != nil {
			report("alias %s not restored: %v", a.Hostname, err)
			continue
		}
		row := &models.WebDomainAlias{ID: ids.NewULID(), DomainID: dom.ID, Hostname: host, CreatedAt: now, UpdatedAt: now}
		if err := domainops.ApplyOwnershipDecision(&row.OwnershipState, restoredAliasOwnership(a), now); err != nil {
			report("alias %s not restored: ownership token: %v", host, err)
			continue
		}
		if err := d.WebDomainAliases.Create(ctx, row); err != nil {
			report("alias %s not restored: %v", host, err)
			continue
		}
		have[aliasKey(host)] = true
		added++
	}
	// The vhost's server_name and the certificate take the new names on the
	// domain's next converge.
	if added > 0 && d.ScheduleDomain != nil {
		d.ScheduleDomain(dom.ID)
	}
}

// aliasKey is an alias hostname as the alias page stores it, for comparing.
func aliasKey(h string) string { return strings.TrimSuffix(domainops.NormalizeDomainName(h), ".") }

// restoredAliasOwnership is restoredOwnership for an alias.
func restoredAliasOwnership(a internalbackup.MetadataDomainAlias) domainops.OwnershipDecision {
	if a.OwnershipStatus != "" && a.OwnershipStatus != models.OwnershipVerified {
		return domainops.OwnershipDecision{}
	}
	return domainops.OwnershipDecision{Verified: true, Method: models.OwnershipMethodRestore}
}
