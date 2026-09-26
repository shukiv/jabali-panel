// Package phases provides a plugin-based reconciliation system for M6 email features.
//
// Each feature (forwarders, autoresponders, catch-all, disclaimer, shared folders, logs)
// registers itself as a Phase during init(). The main reconciler loop runs all registered
// phases in sequence, enabling parallel Wave development without file collisions on
// reconciler.go or router.go.
//
// Phase Interface
//
// Each phase implements:
//   - Name(): string - Unique phase identifier for logging/debugging
//   - ReconcileDomain(ctx, domain, config) error - Converge domain state
//   - ReconcileMailbox(ctx, mailbox, domain, config) error - Converge mailbox state
//
// This pattern extends the mailbox-only reconciliation from M6 (ADR-0041) to encompass
// all email feature state, maintaining jabali-as-truth across the Stalwart integration layer.
//
// Registration
//
// Each feature creates a file under this package:
//   - m65_autoresponders.go: RegisterPhase(&autoresppondersPhase{})
//   - m65_catchall.go: RegisterPhase(&catchallPhase{})
//   - etc.
//
// The registrar is called during each feature's init(), guaranteeing registration
// before the main reconciler loop starts.
//
// NOTE (GH #1795): forwarder convergence does NOT run through this framework.
// Email forwarders (aliases + external redirect Sieve) are converged inline by
// api.applyForwarders on every forwarder mutation; the agent's forwarder.apply
// self-heals the Stalwart Principal if it isn't registered yet. The former
// forwardersPhase here was never wired (RegisterPhase / ReconcileMailboxAll have
// no callers) and shipped a stale payload shape, so it was removed rather than
// left as a resurrection hazard.
//
// Mailbox shares likewise: the former mailboxSharePhase was never registered,
// so nothing ever pushed a share to Stalwart. Shares are applied by
// mailshareops (API/CLI create and delete) and by the reconciler's
// reconcileMailboxShares sweep; the phase was removed for the same reason.
//
// ADR-0051 documents the jabali-as-truth pattern and Stalwart integration for all six features.
package phases
