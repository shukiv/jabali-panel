package mailboxops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ErrSelfServiceRefused is the one answer a self-service password change gives
// for an unknown address, a mailbox that can't sign in, a wrong current
// password, a password this page can't check, or a change that lost a race.
// One answer, so the page tells a guesser nothing about which it was.
var ErrSelfServiceRefused = errors.New("mailboxops: the address or the current password is wrong, or this mailbox's password can't be changed here")

// Eligibility reports whether a mailbox's domain and owner let it sign in to
// the mail server right now. The caller mirrors the mail server's own sign-in
// rule (Stalwart's SQL directory queryLogin: domain ownership verified, owner
// not suspended); ChangeOwnPassword checks the mailbox's own flags itself.
type Eligibility func(ctx context.Context, mb *models.Mailbox) (bool, error)

// ChangeOwnPasswordInput is what the mailbox user typed.
type ChangeOwnPasswordInput struct {
	Email           string
	CurrentPassword string
	NewPassword     string
}

// ChangeOwnPassword lets a mailbox user set a new password by proving the
// current one (GH #2543). Order matters:
//
//  1. The new password's bounds are checked first, before the current
//     password, so a weak new password can't turn the form into an oracle
//     that confirms a guessed password without changing anything.
//  2. The current password is checked against the hash the panel stores,
//     never by signing in to the mail server: the mail server also accepts
//     app passwords, and keeps taking an old password for a while after a
//     change, and neither may be enough to take over the mailbox.
//  3. Only bcrypt hashes are checked here. A migrated mailbox can keep its
//     old panel's hash in another scheme ($6$, argon2, ...); it is refused,
//     and its owner sets a new password in the panel instead.
//  4. A refusal that never reached a real hash still spends one bcrypt
//     comparison, so timing doesn't tell an unknown address from a wrong
//     password.
//  5. The new hash is written only while the stored one is still the hash
//     that was just checked.
//
// System mailboxes (the panel's own sending account) and disabled mailboxes
// are refused. The caller sweeps the mailbox's app passwords and flushes the
// mail server's sign-in cache afterwards, as for a change in the panel.
func ChangeOwnPassword(ctx context.Context, d Deps, eligible Eligibility, in ChangeOwnPasswordInput, notify NotifyFunc) (*models.Mailbox, error) {
	if d.Mailboxes == nil || eligible == nil {
		return nil, fmt.Errorf("%w: mailboxes repo and eligibility required", ErrDeps)
	}
	if err := checkPassword(in.NewPassword); err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || len(email) > 320 || in.CurrentPassword == "" || len(in.CurrentPassword) > PasswordMaxBytes {
		burnCompare(in.CurrentPassword)
		return nil, ErrSelfServiceRefused
	}

	mb, err := d.Mailboxes.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			burnCompare(in.CurrentPassword)
			return nil, ErrSelfServiceRefused
		}
		return nil, fmt.Errorf("%w: lookup: %v", ErrInternal, err)
	}
	if mb.System || mb.IsDisabled || !isBcryptHash(mb.PasswordHash) {
		burnCompare(in.CurrentPassword)
		return nil, ErrSelfServiceRefused
	}
	ok, err := eligible(ctx, mb)
	if err != nil {
		return nil, fmt.Errorf("%w: eligibility: %v", ErrInternal, err)
	}
	if !ok {
		burnCompare(in.CurrentPassword)
		return nil, ErrSelfServiceRefused
	}
	if bcrypt.CompareHashAndPassword([]byte(mb.PasswordHash), []byte(in.CurrentPassword)) != nil {
		return nil, ErrSelfServiceRefused
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("%w: hash: %v", ErrInternal, err)
	}
	enc, err := sealIfKey(d.SSOKey, in.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("%w: seal: %v", ErrInternal, err)
	}
	swapped, err := d.Mailboxes.ReplacePasswordHash(ctx, mb.ID, mb.PasswordHash, string(hash), enc)
	if err != nil {
		return nil, fmt.Errorf("%w: persist: %v", ErrInternal, err)
	}
	if !swapped {
		// The password changed between the check and the write: the newer
		// change stands, and this one is refused like a wrong password.
		return nil, ErrSelfServiceRefused
	}
	if notify != nil {
		notify(ctx, "mailbox.set_password", map[string]any{"id": mb.ID, "email": mb.EmailCached})
	}
	return mb, nil
}

// isBcryptHash reports whether h is a bcrypt hash this package can check.
func isBcryptHash(h string) bool {
	return strings.HasPrefix(h, "$2a$") || strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$")
}

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

// burnCompare spends one bcrypt comparison at the production cost against a
// hash no password matches, so a refusal that never reached a real hash takes
// as long as a wrong password.
func burnCompare(password string) {
	dummyHashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte("jabali-self-service-timing-guard"), BcryptCost)
		if err == nil {
			dummyHash = h
		}
	})
	if dummyHash != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
	}
}
