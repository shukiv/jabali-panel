package mailboxops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// selfServiceRepo records the compare-and-swap ChangeOwnPassword makes.
type selfServiceRepo struct {
	repository.MailboxRepository
	byEmail  map[string]*models.Mailbox
	findErr  error
	swapped  bool // what ReplacePasswordHash answers
	calls    int
	gotOld   string
	gotNew   string
	gotEnc   []byte
	lookedUp string
}

func (r *selfServiceRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	r.lookedUp = email
	if r.findErr != nil {
		return nil, r.findErr
	}
	if mb, ok := r.byEmail[email]; ok {
		return mb, nil
	}
	return nil, repository.ErrNotFound
}

func (r *selfServiceRepo) ReplacePasswordHash(_ context.Context, _ string, oldHash, newHash string, enc []byte) (bool, error) {
	r.calls++
	r.gotOld, r.gotNew, r.gotEnc = oldHash, newHash, enc
	return r.swapped, nil
}

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func allowAll(context.Context, *models.Mailbox) (bool, error) { return true, nil }

func newSelfServiceRepo(t *testing.T, hash string) *selfServiceRepo {
	t.Helper()
	return &selfServiceRepo{
		byEmail: map[string]*models.Mailbox{
			"alice@example.com": {ID: "mb1", EmailCached: "alice@example.com", PasswordHash: hash},
		},
		swapped: true,
	}
}

func TestChangeOwnPassword_Success(t *testing.T) {
	old := hashOf(t, "old-password-1")
	repo := newSelfServiceRepo(t, old)
	key := ssokey.Key{}
	var notified string
	notify := func(_ context.Context, cmd string, _ any) { notified = cmd }

	mb, err := ChangeOwnPassword(context.Background(), Deps{Mailboxes: repo, SSOKey: &key}, allowAll,
		ChangeOwnPasswordInput{Email: "  Alice@Example.COM ", CurrentPassword: "old-password-1", NewPassword: "new-password-2"}, notify)
	if err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	if mb.ID != "mb1" {
		t.Fatalf("mailbox = %q", mb.ID)
	}
	if repo.lookedUp != "alice@example.com" {
		t.Fatalf("looked up %q, want the trimmed lower-case address", repo.lookedUp)
	}
	if repo.gotOld != old {
		t.Fatal("the swap must be conditioned on the hash that was checked")
	}
	if bcrypt.CompareHashAndPassword([]byte(repo.gotNew), []byte("new-password-2")) != nil {
		t.Fatal("the new hash does not match the new password")
	}
	if len(repo.gotEnc) == 0 {
		t.Fatal("with an SSO key the new password must be sealed, so webmail SSO keeps working")
	}
	if notified != "mailbox.set_password" {
		t.Fatalf("notified %q, want mailbox.set_password", notified)
	}
}

// A $2y$ hash (PHP's bcrypt, kept by a migrated mailbox) is still bcrypt.
func TestChangeOwnPassword_AcceptsPHPBcryptPrefix(t *testing.T) {
	h := hashOf(t, "old-password-1")
	repo := newSelfServiceRepo(t, "$2y$"+strings.TrimPrefix(h, "$2a$"))
	if _, err := ChangeOwnPassword(context.Background(), Deps{Mailboxes: repo}, allowAll,
		ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"}, nil); err != nil {
		t.Fatalf("$2y$ hash refused: %v", err)
	}
}

func TestChangeOwnPassword_Refusals(t *testing.T) {
	good := hashOf(t, "old-password-1")
	cases := []struct {
		name     string
		edit     func(*selfServiceRepo)
		eligible Eligibility
		in       ChangeOwnPasswordInput
		want     error
		writes   bool
	}{
		{name: "wrong current password",
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "not-it-at-all", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "unknown address",
			in:   ChangeOwnPasswordInput{Email: "bob@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "empty address",
			in:   ChangeOwnPasswordInput{Email: "  ", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "empty current password",
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "accent variant the database collation matches",
			edit: func(r *selfServiceRepo) { r.byEmail["àlice@example.com"] = r.byEmail["alice@example.com"] },
			in:   ChangeOwnPasswordInput{Email: "àlice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "disabled mailbox",
			edit: func(r *selfServiceRepo) { r.byEmail["alice@example.com"].IsDisabled = true },
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "system mailbox",
			edit: func(r *selfServiceRepo) { r.byEmail["alice@example.com"].System = true },
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "domain or owner can't sign in",
			eligible: func(context.Context, *models.Mailbox) (bool, error) { return false, nil },
			in:       ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want:     ErrSelfServiceRefused},
		{name: "non-bcrypt hash from a migration",
			edit: func(r *selfServiceRepo) { r.byEmail["alice@example.com"].PasswordHash = "$6$salt$abcdef" },
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want: ErrSelfServiceRefused},
		{name: "lost the race to another change",
			edit:   func(r *selfServiceRepo) { r.swapped = false },
			in:     ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"},
			want:   ErrSelfServiceRefused,
			writes: true},
		{name: "new password too short",
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "short"},
			want: ErrWeakPassword},
		{name: "new password too long",
			in:   ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: strings.Repeat("x", 73)},
			want: ErrWeakPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSelfServiceRepo(t, good)
			if tc.edit != nil {
				tc.edit(repo)
			}
			el := tc.eligible
			if el == nil {
				el = allowAll
			}
			_, err := ChangeOwnPassword(context.Background(), Deps{Mailboxes: repo}, el, tc.in, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !tc.writes && repo.calls != 0 {
				t.Fatal("a refused change must not write")
			}
		})
	}
}

// A weak new password is refused before the current password is looked at,
// so the form can't confirm a guessed password without changing anything.
func TestChangeOwnPassword_WeakNewPasswordBeforeAnyLookup(t *testing.T) {
	repo := newSelfServiceRepo(t, hashOf(t, "old-password-1"))
	_, err := ChangeOwnPassword(context.Background(), Deps{Mailboxes: repo}, allowAll,
		ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "short"}, nil)
	if !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("err = %v, want ErrWeakPassword", err)
	}
	if repo.lookedUp != "" {
		t.Fatal("the mailbox was looked up before the new password was checked")
	}
}

func TestChangeOwnPassword_LookupErrorIsInternal(t *testing.T) {
	repo := newSelfServiceRepo(t, hashOf(t, "old-password-1"))
	repo.findErr = errors.New("db down")
	_, err := ChangeOwnPassword(context.Background(), Deps{Mailboxes: repo}, allowAll,
		ChangeOwnPasswordInput{Email: "alice@example.com", CurrentPassword: "old-password-1", NewPassword: "new-password-2"}, nil)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("err = %v, want ErrInternal", err)
	}
}
