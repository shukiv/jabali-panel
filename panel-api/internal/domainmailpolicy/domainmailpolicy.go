// Package domainmailpolicy is the shared Domain Mail Policy lifecycle (JAB-338):
// the catch-all and outbound-disclaimer Set/Clear operations the REST handlers
// and the operator CLI both route through, so the email-enabled gate, the
// canonical target / disclaimer-text validation, the DB→agent call sequence,
// and the best-effort-with-warning semantics have one owner and cannot drift
// between the four previous implementations.
//
// Both features are re-asserted every reconciler tick (phases/m65_catchall.go,
// phases/m65_disclaimer.go read the domains-table columns as truth), so these
// operations persist the DB first and treat the inline agent push as
// best-effort: a failed push is a structured warning, not a hard error — the
// reconciler converges Stalwart on the next tick.
//
// Authorization and response shaping stay in the Adapters (the REST handler
// checks claims; the CLI is admin-by-construction). Every entry point takes an
// already-loaded, already-authorized domain.
package domainmailpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// PushFunc runs one agent command. Its error becomes a warning (never a hard
// failure): the DB is the desired-state truth and the reconciler re-asserts.
type PushFunc func(ctx context.Context, cmd string, params map[string]any) error

// Deps carries the collaborators every operation needs.
type Deps struct {
	Domains repository.DomainRepository
}

var (
	// ErrEmailNotEnabled — mail policy can only be set on a mail-enabled domain.
	// The disclaimer CLI already gated on this; catch-all (HTTP + CLI) did not.
	ErrEmailNotEnabled = errors.New("domainmailpolicy: email is not enabled on the domain")
	// ErrInvalidTarget — the catch-all destination must be a valid email address.
	// Neither adapter validated this before (criterion 2).
	ErrInvalidTarget = errors.New("domainmailpolicy: catch-all target must be a valid email address")
	// ErrDisclaimerTextRequired — an enabled disclaimer needs non-empty text.
	ErrDisclaimerTextRequired = errors.New("domainmailpolicy: an enabled disclaimer needs non-empty text")
	ErrDeps                   = errors.New("domainmailpolicy: dependencies not wired")
	ErrInternal               = errors.New("domainmailpolicy: internal error")
)

// pushWarning message surfaced when the DB write succeeded but the inline agent
// push failed. The reconciler re-asserts within a tick.
const pushWarning = "saved; the mail server did not accept the change yet and will be reconciled shortly"

// canonicalTarget validates a catch-all destination as a syntactically valid
// email and lowercases the DOMAIN half only. Unlike mailaddr.Canonicalise
// (which is for provisioning hosted mailboxes), it must NOT strip +tag
// sub-addressing or restrict the local-part charset: catch-all targets are
// frequently EXTERNAL addresses (e.g. me+catchall@gmail.com), whose local part
// is opaque to us and case- / plus-significant at the receiving provider.
func canonicalTarget(raw string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidTarget, err)
	}
	at := strings.LastIndexByte(addr.Address, '@')
	if at < 0 {
		return "", ErrInvalidTarget
	}
	// Preserve the local part verbatim; lowercase only the (case-insensitive)
	// domain half.
	return addr.Address[:at] + "@" + strings.ToLower(addr.Address[at+1:]), nil
}

// runPush executes the best-effort agent push and returns a warning string when
// it fails (empty on success or when no push func is wired).
func runPush(ctx context.Context, push PushFunc, cmd string, params map[string]any) string {
	if push == nil {
		return ""
	}
	if err := push(ctx, cmd, params); err != nil {
		return pushWarning
	}
	return ""
}

// SetCatchall gates on email-enabled, canonicalizes the target, persists it
// (DB is truth), then pushes best-effort. Returns the canonical target and a
// warning (non-empty only when the push failed).
func SetCatchall(ctx context.Context, d Deps, dom *models.Domain, target string, push PushFunc) (string, string, error) {
	if d.Domains == nil || dom == nil {
		return "", "", fmt.Errorf("%w: domains repo + domain required", ErrDeps)
	}
	if !dom.EmailEnabled {
		return "", "", ErrEmailNotEnabled
	}
	canon, err := canonicalTarget(target)
	if err != nil {
		return "", "", err
	}
	if err := d.Domains.UpdateCatchallTarget(ctx, dom.ID, &canon); err != nil {
		return "", "", fmt.Errorf("%w: persist catch-all: %v", ErrInternal, err)
	}
	warning := runPush(ctx, push, "domain.catchall_set", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
		"target":      canon,
	})
	return canon, warning, nil
}

// ClearCatchall removes the catch-all. Idempotent: clearing an unset domain is
// a success (the column is set to NULL either way).
func ClearCatchall(ctx context.Context, d Deps, dom *models.Domain, push PushFunc) (string, error) {
	if d.Domains == nil || dom == nil {
		return "", fmt.Errorf("%w: domains repo + domain required", ErrDeps)
	}
	if err := d.Domains.UpdateCatchallTarget(ctx, dom.ID, nil); err != nil {
		return "", fmt.Errorf("%w: clear catch-all: %v", ErrInternal, err)
	}
	warning := runPush(ctx, push, "domain.catchall_clear", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	})
	return warning, nil
}

// SetDisclaimer persists the disclaimer's enabled flag + text, then pushes
// best-effort. When enabling it gates on email-enabled and requires non-empty
// normalized text (the REST handler previously skipped the email gate the CLI
// had); when disabling it keeps whatever text was passed (so the UI can toggle
// off without losing the draft) and applies no gate or text requirement. To
// disable AND remove the text, use ClearDisclaimer. Returns the normalized text
// and a warning.
func SetDisclaimer(ctx context.Context, d Deps, dom *models.Domain, enabled bool, text string, push PushFunc) (string, string, error) {
	if d.Domains == nil || dom == nil {
		return "", "", fmt.Errorf("%w: domains repo + domain required", ErrDeps)
	}
	norm := strings.TrimSpace(text)
	if enabled {
		if !dom.EmailEnabled {
			return "", "", ErrEmailNotEnabled
		}
		if norm == "" {
			return "", "", ErrDisclaimerTextRequired
		}
	}
	if err := d.Domains.UpdateDisclaimer(ctx, dom.ID, enabled, &norm); err != nil {
		return "", "", fmt.Errorf("%w: persist disclaimer: %v", ErrInternal, err)
	}
	warning := runPush(ctx, push, "domain.disclaimer_apply", map[string]any{
		"domain_name": dom.Name,
		"enabled":     enabled,
		"text":        norm,
	})
	return norm, warning, nil
}

// ClearDisclaimer disables + empties the disclaimer. Idempotent.
func ClearDisclaimer(ctx context.Context, d Deps, dom *models.Domain, push PushFunc) (string, error) {
	if d.Domains == nil || dom == nil {
		return "", fmt.Errorf("%w: domains repo + domain required", ErrDeps)
	}
	empty := ""
	if err := d.Domains.UpdateDisclaimer(ctx, dom.ID, false, &empty); err != nil {
		return "", fmt.Errorf("%w: clear disclaimer: %v", ErrInternal, err)
	}
	warning := runPush(ctx, push, "domain.disclaimer_apply", map[string]any{
		"domain_name": dom.Name,
		"enabled":     false,
		"text":        "",
	})
	return warning, nil
}
