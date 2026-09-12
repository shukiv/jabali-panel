// Package sharedresourceops is the shared Shared-Resource Lifecycle (JAB-339,
// ADR-0133): the create operation the REST handler and the operator CLI both
// route through, so the email-enabled gate, the kind allowlist, address
// canonicalization, the duplicate-address rule, the trimmed display name, and
// the best-effort agent apply have one owner and cannot drift between the two.
//
// It follows the mailboxops sibling (JAB-291): authorization stays an adapter
// concern — every entry point takes an already-loaded, already-authorized
// domain (the REST handler checks the caller's claims; the operator CLI is
// admin-by-construction). The DB rows are authoritative; the reconciler
// projects each shared_resource into a Stalwart host principal + collection, so
// the agent apply is best-effort and its failure never fails the create.
package sharedresourceops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// kinds is the single source of truth for the shared-resource kind allowlist —
// the REST handler and the CLI used to keep two separate copies. It matches the
// migration's ENUM('mailbox','calendar','addressbook','files').
var kinds = map[string]bool{
	"mailbox": true, "calendar": true, "addressbook": true, "files": true,
}

// ValidKind reports whether kind is an allowed shared-resource kind.
func ValidKind(kind string) bool { return kinds[kind] }

// NotifyFunc is the best-effort agent notify (ADR-0013): its errors are
// swallowed by the caller's implementation and never fail the create.
type NotifyFunc func(ctx context.Context, cmd string, params any)

// Deps carries the collaborators the operations need. Create and Delete use
// Resources; ValidateGrants uses Mailboxes + MailGroups. Each operation guards
// only the deps it needs, so an adapter wires whichever it calls.
type Deps struct {
	Resources  repository.SharedResourceRepository
	Mailboxes  MailboxLookup
	MailGroups MailGroupLookup
}

// MailboxLookup and MailGroupLookup are the narrow existence-lookup slices of
// the mailbox and mail-group repositories that ValidateGrants needs — declared
// consumer-side so the fakes stay small and the module does not pull in the
// full repository interfaces. Both concrete repositories satisfy them.
type MailboxLookup interface {
	FindByID(ctx context.Context, id string) (*models.Mailbox, error)
}
type MailGroupLookup interface {
	FindByID(ctx context.Context, id string) (*models.MailGroup, error)
}

// granteeKinds is the grant GranteeKind allowlist (a grant points at a mailbox
// row or a mail-group row) — distinct from the resource kinds map above. It
// matches the migration's grant ENUM('mailbox','group'). The REST handler and
// the CLI used to keep two separate inline copies of this check.
var granteeKinds = map[string]bool{"mailbox": true, "group": true}

// ValidGranteeKind reports whether kind is an allowed grant grantee kind.
func ValidGranteeKind(kind string) bool { return granteeKinds[kind] }

// Sentinel errors. Adapters map these to their own transport (the HTTP handler
// to specific status codes + body strings, the CLI to a returned error).
var (
	// ErrDeps means a required collaborator was not wired.
	ErrDeps = errors.New("sharedresourceops: dependencies not wired")
	// ErrEmailNotEnabled means the domain does not have email enabled, so a
	// shared resource (an SMTP recipient / DAV principal) cannot be created.
	ErrEmailNotEnabled = errors.New("sharedresourceops: email is not enabled on the domain")
	// ErrInvalidKind means the kind is not in the allowlist.
	ErrInvalidKind = errors.New("sharedresourceops: invalid kind")
	// ErrInvalidName means the requested local part did not canonicalize.
	ErrInvalidName = errors.New("sharedresourceops: invalid name")
	// ErrAddressTaken means a resource already exists at the canonical address.
	ErrAddressTaken = errors.New("sharedresourceops: address already in use")
	// ErrInternal means an unexpected data-access failure.
	ErrInternal = errors.New("sharedresourceops: internal error")

	// Grant validation (ValidateGrants). ErrGranteeInvalidKind means the
	// GranteeKind is not in the allowlist; ErrGranteeMissingID means the
	// GranteeID was empty; ErrGranteeNotFound means the id does not resolve to
	// an existing mailbox / mail-group row.
	ErrGranteeInvalidKind = errors.New("sharedresourceops: invalid grantee kind")
	ErrGranteeMissingID   = errors.New("sharedresourceops: grantee id required")
	ErrGranteeNotFound    = errors.New("sharedresourceops: grantee not found")
)

// CreateInput is one shared-resource creation. The domain is pre-loaded and
// pre-authorized by the adapter (mirroring mailboxops.CreateInput).
type CreateInput struct {
	Domain      *models.Domain
	Kind        string
	Name        string // host address local part
	DisplayName string
}

// Create enforces the EmailEnabled gate + the kind allowlist, canonicalizes the
// address, rejects a duplicate before the insert, persists the row (with a
// trimmed display name), and fires the best-effort agent apply. Returns the
// persisted row.
//
// The order matches the REST handler it replaces: EmailEnabled → kind →
// canonicalise → duplicate → persist. LocalPart / EmailCached are set for every
// kind, exactly as both adapters did before this leaf (the migration reserves
// them for kind='mailbox', but changing that is out of this slice's scope — the
// leaf preserves today's behavior byte-for-byte).
func Create(ctx context.Context, d Deps, in CreateInput, notify NotifyFunc) (*models.SharedResource, error) {
	if d.Resources == nil || in.Domain == nil {
		return nil, fmt.Errorf("%w: resources repo + domain required", ErrDeps)
	}
	if !in.Domain.EmailEnabled {
		return nil, ErrEmailNotEnabled
	}
	if !ValidKind(in.Kind) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidKind, in.Kind)
	}
	canonLocal, _, err := mailaddr.Canonicalise(in.Name + "@" + in.Domain.Name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidName, err)
	}
	email := canonLocal + "@" + in.Domain.Name
	// Pre-INSERT duplicate check: replaces the raw UNIQUE-constraint driver
	// error with a typed one. The repository does NOT map the constraint to
	// ErrConflict, so the narrow race between this check and the insert still
	// surfaces as ErrInternal — the pre-check covers the common case, not the
	// race.
	if exists, err := d.Resources.ExistsByEmail(ctx, email); err != nil {
		return nil, fmt.Errorf("%w: uniqueness check: %v", ErrInternal, err)
	} else if exists {
		return nil, ErrAddressTaken
	}

	now := time.Now().UTC()
	local := canonLocal
	sr := &models.SharedResource{
		ID:          ids.NewULID(),
		DomainID:    in.Domain.ID,
		Kind:        in.Kind,
		LocalPart:   &local,
		EmailCached: &email,
		DisplayName: strings.TrimSpace(in.DisplayName),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := d.Resources.Create(ctx, sr); err != nil {
		return nil, fmt.Errorf("%w: insert: %v", ErrInternal, err)
	}

	// Instant host provisioning (reconciler also converges from DB truth).
	if notify != nil {
		notify(ctx, "sharedresource.apply", map[string]any{
			"email": email, "display_name": sr.DisplayName, "kind": sr.Kind,
		})
	}
	return sr, nil
}

// DeleteInput is one shared-resource deletion. The resource is pre-loaded and
// pre-authorized by the adapter (the REST handler checks the caller's claims;
// the operator CLI is admin-by-construction), mirroring CreateInput's
// pre-loaded domain.
type DeleteInput struct {
	Resource *models.SharedResource
}

// Delete tears a shared resource down in the order AC5 requires: it records a
// DURABLE tombstone first, then fires the best-effort agent destroy, then
// deletes the row.
//
// The tombstone is the reconciler GC's only handle on the Stalwart host
// principal — the reconciler does not scan for orphaned hosts — so if the
// tombstone cannot be persisted the row is LEFT in place (ErrInternal) instead
// of deleted: deleting it would strand the principal with no path to ever
// reclaim it. This is stricter than the handlers it replaces, which swallowed
// the tombstone error and deleted the row regardless. AddTombstone is
// idempotent (OnConflict DoNothing), so retrying after a later failure re-adds
// the same tombstone harmlessly.
//
// A resource with no cached address (EmailCached empty) has no host principal
// to tear down, so it skips straight to the row delete.
func Delete(ctx context.Context, d Deps, in DeleteInput, notify NotifyFunc) error {
	if d.Resources == nil || in.Resource == nil {
		return fmt.Errorf("%w: resources repo + resource required", ErrDeps)
	}
	sr := in.Resource
	if sr.EmailCached != nil && *sr.EmailCached != "" {
		if err := d.Resources.AddTombstone(ctx, *sr.EmailCached); err != nil {
			return fmt.Errorf("%w: tombstone: %v", ErrInternal, err)
		}
		if notify != nil {
			notify(ctx, "sharedresource.destroy", map[string]any{"email": *sr.EmailCached})
		}
	}
	if err := d.Resources.Delete(ctx, sr.ID); err != nil {
		return fmt.Errorf("%w: delete: %v", ErrInternal, err)
	}
	return nil
}

// ValidateGrants is the JAB-339 AC4 grantee-existence gate the REST handler and
// the operator CLI both run before ReplaceGrants. It checks every grant
// references (1) an allowed grantee kind, (2) a non-empty grantee id, and (3) —
// the gap this closes — an EXISTING mailbox / mail-group row.
//
// Why existence matters: a grant to a non-existent grantee id used to persist
// with a 200 / success, and the reconciler discovers the dangling reference
// only later — its grant loop `continue`s on a FindByID not-found, so the bad
// grantee is silently dropped from the projected shareWith on every pass, with
// no error surfaced anywhere. The reference is invisible until someone reads
// the grant list back. Rejecting it at write time is the fail-loud fix.
//
// Lookup failures fail CLOSED: a not-found row is ErrGranteeNotFound (reject
// the whole set — ReplaceGrants is an all-or-nothing replace), but any OTHER
// data-access error is wrapped ErrInternal and propagated, never collapsed into
// "not found". Collapsing would let a grant slip through on a transient DB
// blip. Kind + id are folded in here too: the REST handler drops its inline
// copies, while the CLI keeps its flag-specific messages first and lets this
// re-check them harmlessly — so one owner governs the policy for both.
//
// Domain / tenant scoping of the grantee is deliberately NOT enforced here: a
// cross-domain grant is a sharing-scope policy question, not a privilege
// escalation (the caller can only share a resource it already owns, and the
// grantee gains nothing on its own tenant), and which domains a grantee may
// come from is an open product decision. When that rule lands it belongs in
// this same validator and MUST return ErrGranteeNotFound too — a grantee that
// exists but is out of policy must be indistinguishable from a missing one, so
// the error can never be used to enumerate rows across a tenant boundary.
func ValidateGrants(ctx context.Context, d Deps, grants []models.SharedResourceGrant) error {
	if d.Mailboxes == nil || d.MailGroups == nil {
		return fmt.Errorf("%w: mailbox + mail-group lookups required", ErrDeps)
	}
	for _, g := range grants {
		if !ValidGranteeKind(g.GranteeKind) {
			return fmt.Errorf("%w: %s", ErrGranteeInvalidKind, g.GranteeKind)
		}
		if g.GranteeID == "" {
			return ErrGranteeMissingID
		}
		switch g.GranteeKind {
		case "mailbox":
			if _, err := d.Mailboxes.FindByID(ctx, g.GranteeID); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("%w: mailbox %s", ErrGranteeNotFound, g.GranteeID)
				}
				return fmt.Errorf("%w: mailbox lookup %s: %v", ErrInternal, g.GranteeID, err)
			}
		case "group":
			if _, err := d.MailGroups.FindByID(ctx, g.GranteeID); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("%w: group %s", ErrGranteeNotFound, g.GranteeID)
				}
				return fmt.Errorf("%w: group lookup %s: %v", ErrInternal, g.GranteeID, err)
			}
		}
	}
	return nil
}
