package userops

import (
	"context"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ChownReconciler re-renders a domain after its owner — and thus its docroot and
// PHP pool binding — changed. Satisfied by *reconciler.Reconciler.
type ChownReconciler interface {
	Schedule(domainID string)
}

// ChownDeps carries the extra repos ChangeDomainOwner needs beyond the core Deps.
type ChownDeps struct {
	AppInstalls repository.ApplicationInstallRepository
	Reconciler  ChownReconciler
}

// ChangeDomainOwner reassigns a domain to a new tenant, moving its docroot into
// the new owner's home and re-owning the files to the new uid (GH #1238).
//
// The domain row is never deleted/recreated — domain_id survives, so tombstones,
// the SSL lineage, the DNS zone, and mailboxes all stay. Files move first (agent
// domain.reown), then the DB repoints owner + docroot; the running reconciler
// re-binds the PHP pool and re-renders the vhost under the new owner.
//
// v1 REFUSES a domain that has an app install: its config (e.g. WordPress
// wp-config) carries the OLD owner's database credentials, so handing the files
// to the new owner would leak a live cross-tenant credential. Detach or migrate
// the app + its database to the new owner first.
func ChangeDomainOwner(ctx context.Context, d Deps, cd ChownDeps, domain *models.Domain, newOwner *models.User) error {
	if domain == nil || newOwner == nil {
		return fmt.Errorf("chown: nil domain or new owner")
	}
	if newOwner.IsAdmin || newOwner.Username == nil || *newOwner.Username == "" {
		return fmt.Errorf("chown: the new owner must be a tenant with a Linux username")
	}
	if newOwner.LinuxUID == nil {
		return fmt.Errorf("chown: new owner %q has no linux_uid yet (account not fully provisioned)", *newOwner.Username)
	}
	if domain.UserID == newOwner.ID {
		return fmt.Errorf("chown: %q is already owned by %q", domain.Name, *newOwner.Username)
	}
	if d.Users == nil || d.Domains == nil || d.Agent == nil {
		return fmt.Errorf("chown: users, domains, and agent must be wired")
	}

	// Refuse when an app install is present (cross-tenant DB-credential leak).
	if cd.AppInstalls != nil {
		if inst, err := cd.AppInstalls.FindByDomainID(ctx, domain.ID); err == nil && inst != nil {
			return fmt.Errorf("chown: %q has an app install (%s) whose config holds the current owner's database credentials — detach or migrate it to the new owner first", domain.Name, inst.AppType)
		}
	}

	// Resolve the old owner to derive the /home/<old>/ prefix embedded in the docroot.
	oldOwner, err := d.Users.FindByID(ctx, domain.UserID)
	if err != nil || oldOwner == nil || oldOwner.Username == nil || *oldOwner.Username == "" {
		return fmt.Errorf("chown: could not resolve the current owner of %q: %w", domain.Name, err)
	}
	oldPrefix := "/home/" + *oldOwner.Username + "/"
	newPrefix := "/home/" + *newOwner.Username + "/"
	if !strings.HasPrefix(domain.DocRoot, oldPrefix) {
		return fmt.Errorf("chown: docroot %q is not under the current owner's home %q — refusing (needs manual review)", domain.DocRoot, oldPrefix)
	}
	newDocRoot := newPrefix + strings.TrimPrefix(domain.DocRoot, oldPrefix)

	// Move + re-own the docroot tree on the box.
	if _, err := d.Agent.Call(ctx, "domain.reown", map[string]any{
		"old_doc_root": domain.DocRoot,
		"new_doc_root": newDocRoot,
		"new_uid":      int(*newOwner.LinuxUID),
	}); err != nil {
		return fmt.Errorf("chown: agent domain.reown failed: %w", err)
	}

	// Repoint the DB (files already moved past this line).
	if err := d.Domains.TransferOwner(ctx, domain.ID, newOwner.ID, newDocRoot); err != nil {
		return fmt.Errorf("chown: persist new owner (files already moved to %q — re-run to resync the DB): %w", newDocRoot, err)
	}

	if cd.Reconciler != nil {
		cd.Reconciler.Schedule(domain.ID)
	}
	domain.UserID = newOwner.ID
	domain.DocRoot = newDocRoot
	return nil
}
