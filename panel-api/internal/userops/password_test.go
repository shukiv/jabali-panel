package userops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestCheckPasswordLength(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		tooLong  bool
	}{
		{"72 ASCII bytes", strings.Repeat("a", 72), false},
		{"73 ASCII bytes", strings.Repeat("a", 73), true},
		{"36 two-byte letters (72 bytes)", strings.Repeat("ש", 36), false},
		{"25 three-byte letters (75 bytes)", strings.Repeat("日", 25), true},
	} {
		err := CheckPasswordLength(tc.password)
		if tc.tooLong != errors.Is(err, ErrPasswordTooLong) {
			t.Errorf("%s: err = %v, want too long = %v", tc.name, err, tc.tooLong)
		}
	}
}

// A password bcrypt cannot hash is the caller's input: Create says so, and
// says it before anything is created.
func TestCreate_PasswordOverBcryptLimitIsRefusedFirst(t *testing.T) {
	name := "tenant1"
	users := &createUsers{}
	agent := &syncAgent{}
	_, err := Create(context.Background(), createDeps(users, agent), CreateInput{
		Email: "t1@example.com", Password: strings.Repeat("p", 73), Username: &name,
	})
	if !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("err = %v, want ErrPasswordTooLong", err)
	}
	if users.created != nil {
		t.Fatalf("a user row was created: %+v", users.created)
	}
	if len(agent.methods) != 0 {
		t.Fatalf("agent was called: %v", agent.methods)
	}
}

func TestRotatePassword_PasswordOverBcryptLimitIsRefused(t *testing.T) {
	kid := "kratos-id"
	user := &models.User{ID: "u1", KratosIdentityID: &kid}
	deps := Deps{Users: &createUsers{}, BcryptCost: 4}

	err := RotatePassword(context.Background(), deps, user, strings.Repeat("日", 25))
	if !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("75-byte password: err = %v, want ErrPasswordTooLong", err)
	}
	// 72 bytes still hashes; with no Kratos client wired it stops there.
	err = RotatePassword(context.Background(), deps, user, strings.Repeat("a", 72))
	if !errors.Is(err, ErrKratosUnavailable) {
		t.Fatalf("72-byte password: err = %v, want ErrKratosUnavailable (hashed, then no Kratos)", err)
	}
}
