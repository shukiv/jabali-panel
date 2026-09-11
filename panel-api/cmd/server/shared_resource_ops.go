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
