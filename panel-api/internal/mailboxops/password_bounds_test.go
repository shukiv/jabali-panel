package mailboxops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// A mailbox password is an IMAP/SMTP/webmail credential anyone on the internet
// can try. A caller-supplied one must be at least 8 characters (the minimum
// both mailbox forms already ask for) and at most 72 bytes (bcrypt's input
// limit: a longer one failed to hash and answered 500). Empty still means
// "generate one".
var (
	weakPasswords = map[string]string{
		"one character":         "a",
		"seven characters":      "abc1234",
		"seven 2-byte runes":    strings.Repeat("é", 7),
		"73 bytes":              strings.Repeat("x", 73),
		"37 2-byte runes (74B)": strings.Repeat("é", 37),
	}
	acceptedPasswords = map[string]string{
		"eight characters":      "abcd1234",
		"eight 2-byte runes":    strings.Repeat("é", 8),
		"72 bytes":              strings.Repeat("x", 72),
		"36 2-byte runes (72B)": strings.Repeat("é", 36),
	}
)

func TestCreate_PasswordBounds(t *testing.T) {
	key := ssokey.Key{}
	create := func(pw string) (*fakeMBRepo, error) {
		repo := &fakeMBRepo{}
		_, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key},
			CreateInput{Domain: enabledDomain(), LocalPart: "alice", Password: pw}, nil)
		return repo, err
	}
	for name, pw := range weakPasswords {
		repo, err := create(pw)
		if !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%s: err = %v, want ErrWeakPassword", name, err)
		}
		if repo.created != nil {
			t.Errorf("%s: a mailbox was created with a rejected password", name)
		}
	}
	for name, pw := range acceptedPasswords {
		if _, err := create(pw); err != nil {
			t.Errorf("%s: err = %v, want accepted", name, err)
		}
	}
	if _, err := create(""); err != nil {
		t.Errorf("empty password must still generate one, got %v", err)
	}
}

func TestRotate_PasswordBounds(t *testing.T) {
	rotate := func(pw string) (*fakeMBRepo, error) {
		repo := &fakeMBRepo{existing: map[string]*models.Mailbox{"a@example.com": {ID: "m1"}}}
		_, err := RotatePassword(context.Background(), Deps{Mailboxes: repo}, "a@example.com", pw, nil)
		return repo, err
	}
	for name, pw := range weakPasswords {
		repo, err := rotate(pw)
		if !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%s: err = %v, want ErrWeakPassword", name, err)
		}
		if repo.encCalled {
			t.Errorf("%s: the rejected password was written", name)
		}
	}
	for name, pw := range acceptedPasswords {
		if _, err := rotate(pw); err != nil {
			t.Errorf("%s: err = %v, want accepted", name, err)
		}
	}
	if _, err := rotate(""); err != nil {
		t.Errorf("empty password must still generate one, got %v", err)
	}
}
