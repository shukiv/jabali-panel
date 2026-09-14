package main

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sharedresourceops"
)

const cliSharedResourceAgentTimeout = 15 * time.Second

// createSharedResourceDirect mirrors POST /domains/:id/shared-resources: the
// email-enabled gate, kind allowlist, canonicalisation, duplicate-address
// check, trimmed display name, persist, and best-effort agent apply all live in
// sharedresourceops, so the CLI enforces the exact same policy the REST handler
// does and both project identical state.
func createSharedResourceDirect(ctx context.Context, repo repository.SharedResourceRepository, notify agentNotifier, dom *models.Domain, kind, name, displayName string) (*models.SharedResource, error) {
	return sharedresourceops.Create(ctx, sharedresourceops.Deps{Resources: repo}, sharedresourceops.CreateInput{
		Domain:      dom,
		Kind:        kind,
		Name:        name,
		DisplayName: displayName,
	}, sharedresourceops.NotifyFunc(notify))
}

// deleteSharedResourceDirect mirrors DELETE /shared-resources/:rid: it loads the
// resource, then routes the durable-tombstone-before-best-effort-destroy
// teardown through sharedresourceops.Delete so the CLI and REST tear a resource
// down identically (JAB-339 AC5). A load error is returned rather than swallowed
// — the old CLI skipped teardown on a FindByID error yet still deleted the row,
// orphaning the Stalwart host principal with no tombstone for the reconciler GC.
func deleteSharedResourceDirect(ctx context.Context, repo repository.SharedResourceRepository, notify agentNotifier, resourceID string) error {
	sr, err := repo.FindByID(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("load resource: %w", err)
	}
	return sharedresourceops.Delete(ctx, sharedresourceops.Deps{Resources: repo}, sharedresourceops.DeleteInput{
		Resource: sr,
	}, sharedresourceops.NotifyFunc(notify))
}

// grantSharedResourceDirect mirrors PUT /shared-resources/:rid/grants: it lists
// the current grant set, upserts the requested grantee into it, validates the
// FULL resulting set through sharedresourceops.ValidateGrants — not just the new
// delta — and only then replaces the set (JAB-339 AC3). Validating the whole
// resulting set is exactly what the REST setGrants door does (it validates the
// entire replacement body it receives), so a CLI grant that would leave a
// pre-existing cross-owner or now-missing grantee in the set fails loud here too
// instead of silently persisting it. Both doors therefore project identical
// state from equivalent inputs.
//
// The old CLI validated only the single new grant, so it could upsert a valid
// grantee onto a set that still carried a legacy invalid one and write it back —
// the REST door, validating the full set, rejected the same operation. Revoke is
// deliberately NOT validated the same way: it only removes access, and rejecting
// a revoke because a *different* legacy grant is invalid would refuse to shrink
// the set — the wrong direction for a clamp, and the escape hatch that lets an
// operator repair a resource the tightened grant path now refuses to extend.
func grantSharedResourceDirect(ctx context.Context, repo repository.SharedResourceRepository, grantDeps sharedresourceops.Deps, ownerUserID, resourceID, granteeKind, granteeID, rights string) error {
	grants, err := repo.ListGrants(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("list grants: %w", err)
	}
	next := make([]models.SharedResourceGrant, 0, len(grants)+1)
	for _, g := range grants {
		if g.GranteeKind == granteeKind && g.GranteeID == granteeID {
			continue // replaced below
		}
		next = append(next, g)
	}
	next = append(next, models.SharedResourceGrant{
		ResourceID: resourceID, GranteeKind: granteeKind, GranteeID: granteeID, Rights: rights,
	})
	// Validate the full resulting set, not just the delta. ValidateGrants wraps
	// the offending grantee id, so the returned error names whichever grant is
	// bad — which may be a pre-existing one, not the one just requested.
	if err := sharedresourceops.ValidateGrants(ctx, grantDeps, ownerUserID, next); err != nil {
		return err
	}
	if err := repo.ReplaceGrants(ctx, resourceID, next); err != nil {
		return fmt.Errorf("replace grants: %w", err)
	}
	return nil
}

// notifyAgentSharedResource is the production agentNotifier wired off the global
// sharedAgent. Swallows errors — ADR-0013 best-effort; the reconciler converges
// from DB truth regardless.
func notifyAgentSharedResource(ctx context.Context, cmd string, params any) {
	if sharedAgent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, cliSharedResourceAgentTimeout)
	defer cancel()
	_, _ = sharedAgent.Call(agentCtx, cmd, params)
}
