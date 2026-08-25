// Package autoresponderops is the shared Mailbox Autoresponder Lifecycle
// (JAB-346): the Set / Clear operations — and the one canonical agent-parameter
// projection — that the REST handler, the operator CLI, and the reconciler
// phase all route through, so the content policy, the date-range rule, and the
// "autoresponder.set" payload have a single owner and cannot drift between the
// three callers.
//
// Authorization stays an Adapter concern: the REST handler checks the caller's
// claims and the CLI is admin-by-construction; every entry point here takes an
// already-loaded, already-authorized mailbox identity.
package autoresponderops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const managedBy = "m6.5"

// PushFunc pushes one agent command. Its error is treated as a SOFT failure by
// Set/Clear: the DB is the desired-state truth and the reconciler re-asserts on
// the next tick, so a push error becomes a structured retry warning, never a
// lost change.
type PushFunc func(ctx context.Context, cmd string, params map[string]any) error

// Deps carries the collaborators every operation needs.
type Deps struct {
	Autoresponders repository.EmailAutoresponderRepository
}

var (
	// ErrContentRequired — an ENABLED autoresponder must carry a subject and at
	// least one body. The REST handler used to persist an enabled-but-empty
	// responder (the CLI already rejected it); this closes that drift.
	ErrContentRequired = errors.New("autoresponderops: an enabled autoresponder needs a subject and a text or HTML body")
	// ErrInvalidDateRange — from_date must be on or before to_date.
	ErrInvalidDateRange = errors.New("autoresponderops: from_date must be on or before to_date")
	ErrDeps             = errors.New("autoresponderops: dependencies not wired")
	ErrInternal         = errors.New("autoresponderops: internal error")
)

// SetInput is one autoresponder upsert. MailboxEmail is the canonical address
// the agent keys the Stalwart VacationResponse on; MailboxID is the DB key.
type SetInput struct {
	MailboxID    string
	MailboxEmail string
	Enabled      bool
	Subject      *string
	TextBody     *string
	HTMLBody     *string
	FromDate     *time.Time
	ToDate       *time.Time
}

// Validate enforces the content policy and the date-range rule for EVERY
// adapter. Exposed so a caller can pre-validate before touching the DB if it
// wants; Set calls it too.
func Validate(in SetInput) error {
	if in.FromDate != nil && in.ToDate != nil && in.FromDate.After(*in.ToDate) {
		return ErrInvalidDateRange
	}
	if in.Enabled {
		if in.Subject == nil || strings.TrimSpace(*in.Subject) == "" {
			return ErrContentRequired
		}
		hasText := in.TextBody != nil && strings.TrimSpace(*in.TextBody) != ""
		hasHTML := in.HTMLBody != nil && strings.TrimSpace(*in.HTMLBody) != ""
		if !hasText && !hasHTML {
			return ErrContentRequired
		}
	}
	return nil
}

// AgentParams is the SINGLE canonical "autoresponder.set" payload projection.
// The reconciler phase, Set, and (transitively) every adapter build the agent
// request through this, so their parameters are byte-identical (JAB-346
// criterion 3).
func AgentParams(mailboxEmail string, ar *models.EmailAutoresponder) map[string]any {
	params := map[string]any{
		"mailbox_email": mailboxEmail,
		"enabled":       ar.Enabled,
	}
	if ar.FromDate != nil {
		params["from_date"] = ar.FromDate.UTC().Format(time.RFC3339)
	}
	if ar.ToDate != nil {
		params["to_date"] = ar.ToDate.UTC().Format(time.RFC3339)
	}
	if ar.Subject != nil {
		params["subject"] = *ar.Subject
	}
	if ar.TextBody != nil {
		params["text_body"] = *ar.TextBody
	}
	if ar.HTMLBody != nil {
		params["html_body"] = *ar.HTMLBody
	}
	return params
}

// Set validates the intake, persists the desired state (DB is truth), then does
// a best-effort agent push. Returns the persisted row and a WARNING string that
// is non-empty only when the DB write succeeded but the agent push failed — the
// caller surfaces it as "saved; the mail server will catch up on the next
// reconcile" rather than a hard error (JAB-346 criterion 5). A validation or
// persistence failure is a hard error and nothing is pushed.
func Set(ctx context.Context, d Deps, in SetInput, push PushFunc) (*models.EmailAutoresponder, string, error) {
	if d.Autoresponders == nil {
		return nil, "", fmt.Errorf("%w: autoresponders repo required", ErrDeps)
	}
	if err := Validate(in); err != nil {
		return nil, "", err
	}
	ar := &models.EmailAutoresponder{
		MailboxID: in.MailboxID,
		Enabled:   in.Enabled,
		FromDate:  in.FromDate,
		ToDate:    in.ToDate,
		Subject:   in.Subject,
		TextBody:  in.TextBody,
		HTMLBody:  in.HTMLBody,
		ManagedBy: managedBy,
	}
	if err := d.Autoresponders.Update(ctx, ar); err != nil {
		return nil, "", fmt.Errorf("%w: persist: %v", ErrInternal, err)
	}
	warning := ""
	if push != nil {
		if perr := push(ctx, "autoresponder.set", AgentParams(in.MailboxEmail, ar)); perr != nil {
			warning = "saved; the mail server did not accept the change yet and will be reconciled shortly"
		}
	}
	return ar, warning, nil
}

// Clear disables + deletes the autoresponder. Idempotent (JAB-346 criterion 4):
// a missing row is not an error, and the agent disable is a no-op when already
// off. The DB delete is authoritative; the agent push is best-effort.
func Clear(ctx context.Context, d Deps, mailboxID, mailboxEmail string, push PushFunc) error {
	if d.Autoresponders == nil {
		return fmt.Errorf("%w: autoresponders repo required", ErrDeps)
	}
	if err := d.Autoresponders.Delete(ctx, mailboxID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("%w: delete: %v", ErrInternal, err)
	}
	if push != nil {
		// Disable on the mail server too; best-effort, reconciler re-asserts.
		_ = push(ctx, "autoresponder.set", map[string]any{
			"mailbox_email": mailboxEmail,
			"enabled":       false,
		})
	}
	return nil
}
