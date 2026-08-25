// Package mailboxops is the shared Mailbox Lifecycle (JAB-291): the create,
// password-rotation, quota, and delete operations the REST handlers and the
// operator CLI both route through, so address canonicalization, the quota
// default/floor, password generation/hash/sealing, the duplicate rule, and the
// display_name/send_only fields have one owner and cannot drift between the two.
//
// Authorization stays an Adapter concern: every entry point takes an already-
// loaded, already-authorized domain/mailbox (the REST handler checks the
// caller's claims; the operator CLI is admin-by-construction). System mailboxes
// (the GH #1056 noreply relay) are created only by the reconciler and are NOT
// expressible here; the migration restore path keeps its own semantics too.
package mailboxops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// The single source for these constants — the CLI used to duplicate them with a
// "keep in sync" comment.
const (
	DefaultQuotaBytes uint64 = 1 << 30          // 1 GiB
	MinQuotaBytes     uint64 = 16 * 1024 * 1024 // 16 MiB floor
	BcryptCost               = bcrypt.DefaultCost
)

// NotifyFunc is the best-effort agent notify (ADR-0013): its errors are
// swallowed by the caller's implementation and never fail the operation.
type NotifyFunc func(ctx context.Context, cmd string, params any)

// CallFunc is the hard agent call used by Delete — its error aborts before the
// row is removed so Stalwart and the DB never diverge.
type CallFunc func(ctx context.Context, cmd string, params any) (json.RawMessage, error)

// Deps carries the collaborators every operation needs.
type Deps struct {
	Mailboxes repository.MailboxRepository
	// SSOKey seals the plaintext into password_enc for the webmail SSO flow.
	// Nil → the row is stored/rotated with NO envelope (SSO unavailable until a
	// rotate with a live key), never a stale one.
	SSOKey *ssokey.Key
}

var (
	ErrEmailNotEnabled  = errors.New("mailboxops: email is not enabled on the domain")
	ErrInvalidLocalPart = errors.New("mailboxops: invalid local part")
	ErrMailboxExists    = errors.New("mailboxops: mailbox already exists")
	ErrQuotaTooSmall    = errors.New("mailboxops: quota below the 16 MiB floor")
	ErrNotFound         = errors.New("mailboxops: mailbox not found")
	ErrAgentUnavailable = errors.New("mailboxops: agent not configured")
	ErrDeps             = errors.New("mailboxops: dependencies not wired")
	ErrInternal         = errors.New("mailboxops: internal error")
)

// CreateInput is one interactive mailbox creation. Note the ABSENCE of a System
// field — user-facing creates must never mint an infrastructure principal.
type CreateInput struct {
	Domain      *models.Domain // pre-loaded + authorized by the Adapter
	LocalPart   string
	Password    string // "" → generate a reveal-once secret
	QuotaBytes  uint64 // 0 → DefaultQuotaBytes
	DisplayName string
	SendOnly    bool
}

// Create canonicalizes the address, enforces the EmailEnabled gate + the
// duplicate rule + the quota default/floor, generates/hashes/seals the password,
// persists the row (with display_name + send_only), and fires the best-effort
// agent notify. Returns the row and the generated password ("" when the caller
// supplied one — the reveal-once contract).
func Create(ctx context.Context, d Deps, in CreateInput, notify NotifyFunc) (*models.Mailbox, string, error) {
	if d.Mailboxes == nil || in.Domain == nil {
		return nil, "", fmt.Errorf("%w: mailboxes repo + domain required", ErrDeps)
	}
	if !in.Domain.EmailEnabled {
		return nil, "", ErrEmailNotEnabled
	}
	canonLocal, _, err := mailaddr.Canonicalise(in.LocalPart + "@" + in.Domain.Name)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidLocalPart, err)
	}
	exists, err := d.Mailboxes.ExistsByDomainAndLocalPart(ctx, in.Domain.ID, canonLocal)
	if err != nil {
		return nil, "", fmt.Errorf("%w: uniqueness check: %v", ErrInternal, err)
	}
	if exists {
		return nil, "", ErrMailboxExists
	}

	password := in.Password
	generated := ""
	if password == "" {
		password = ids.NewSecret()
		generated = password
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("%w: hash: %v", ErrInternal, err)
	}

	quota := in.QuotaBytes
	if quota == 0 {
		quota = DefaultQuotaBytes
	}
	if quota < MinQuotaBytes {
		return nil, "", ErrQuotaTooSmall
	}

	enc, err := sealIfKey(d.SSOKey, password)
	if err != nil {
		return nil, "", fmt.Errorf("%w: seal: %v", ErrInternal, err)
	}

	now := time.Now().UTC()
	mb := &models.Mailbox{
		ID:           ids.NewULID(),
		DomainID:     in.Domain.ID,
		LocalPart:    canonLocal,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		PasswordHash: string(hash),
		PasswordEnc:  enc,
		QuotaBytes:   quota,
		SendOnly:     in.SendOnly,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := d.Mailboxes.Create(ctx, mb); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, "", ErrMailboxExists
		}
		return nil, "", fmt.Errorf("%w: insert: %v", ErrInternal, err)
	}

	email := canonLocal + "@" + in.Domain.Name
	if notify != nil {
		notify(ctx, "mailbox.create", map[string]any{"id": mb.ID, "email": email, "display_name": mb.DisplayName})
	}
	// The BEFORE INSERT trigger already wrote email_cached in the DB; reflect it
	// on the returned struct so the caller's response is correct without a re-read.
	mb.EmailCached = email
	return mb, generated, nil
}

// RotatePassword rotates a mailbox's password. Empty newPassword → generate one
// and return it once. The hash and the sealed envelope are updated ATOMICALLY:
// with a live SSO key the envelope is re-sealed to the new password; WITHOUT a
// key it is CLEARED (password_enc = NULL), never left stale — a stale envelope
// makes the webmail SSO mint decrypt to the OLD password Stalwart no longer
// accepts (this closes a live bug where both adapters called the hash-only
// UpdatePasswordHash, leaving the old envelope behind).
func RotatePassword(ctx context.Context, d Deps, email, newPassword string, notify NotifyFunc) (string, error) {
	if d.Mailboxes == nil {
		return "", fmt.Errorf("%w: mailboxes repo required", ErrDeps)
	}
	mb, err := d.Mailboxes.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("%w: lookup: %v", ErrInternal, err)
	}
	generated := ""
	if newPassword == "" {
		newPassword = ids.NewSecret()
		generated = newPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("%w: hash: %v", ErrInternal, err)
	}
	enc, err := sealIfKey(d.SSOKey, newPassword)
	if err != nil {
		return "", fmt.Errorf("%w: seal: %v", ErrInternal, err)
	}
	// enc is nil when no key → password_enc is set to NULL (never left stale).
	if err := d.Mailboxes.UpdatePasswordHashAndEnc(ctx, mb.ID, string(hash), enc); err != nil {
		return "", fmt.Errorf("%w: persist: %v", ErrInternal, err)
	}
	if notify != nil {
		notify(ctx, "mailbox.set_password", map[string]any{"id": mb.ID, "email": email})
	}
	return generated, nil
}

// SetQuota updates a mailbox's quota (floor: MinQuotaBytes).
func SetQuota(ctx context.Context, d Deps, email string, quotaBytes uint64, notify NotifyFunc) (*models.Mailbox, error) {
	if d.Mailboxes == nil {
		return nil, fmt.Errorf("%w: mailboxes repo required", ErrDeps)
	}
	if quotaBytes < MinQuotaBytes {
		return nil, ErrQuotaTooSmall
	}
	mb, err := d.Mailboxes.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: lookup: %v", ErrInternal, err)
	}
	if err := d.Mailboxes.UpdateQuota(ctx, mb.ID, quotaBytes); err != nil {
		return nil, fmt.Errorf("%w: update quota: %v", ErrInternal, err)
	}
	if notify != nil {
		notify(ctx, "mailbox.set_quota", map[string]any{"id": mb.ID, "email": email, "quota_bytes": quotaBytes})
	}
	mb.QuotaBytes = quotaBytes
	mb.UpdatedAt = time.Now().UTC()
	return mb, nil
}

// Delete destroys the Stalwart account FIRST (a hard dependency) then removes
// the row — a failed destroy aborts before the row delete so the DB never
// tombstones a mailbox whose Stalwart side is still live.
func Delete(ctx context.Context, mailboxes repository.MailboxRepository, call CallFunc, email string) error {
	if mailboxes == nil {
		return fmt.Errorf("%w: mailboxes repo required", ErrDeps)
	}
	mb, err := mailboxes.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: lookup: %v", ErrInternal, err)
	}
	if call == nil {
		return ErrAgentUnavailable
	}
	if _, err := call(ctx, "mailbox.delete", map[string]any{"id": mb.ID, "email": email}); err != nil {
		return fmt.Errorf("%w: agent mailbox.delete: %v", ErrInternal, err)
	}
	if err := mailboxes.Delete(ctx, mb.ID); err != nil {
		return fmt.Errorf("%w: delete row: %v", ErrInternal, err)
	}
	return nil
}

func sealIfKey(key *ssokey.Key, password string) ([]byte, error) {
	if key == nil {
		return nil, nil
	}
	return key.Seal([]byte(password))
}
