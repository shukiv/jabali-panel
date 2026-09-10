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

// Deps carries the collaborators Create needs.
type Deps struct {
	Resources repository.SharedResourceRepository
}

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
