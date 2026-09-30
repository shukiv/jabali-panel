package userops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// createUsers stores users like the real table since migration 000164: a row
// without a username is refused.
type createUsers struct {
	repository.UserRepository
	created *models.User
}

func (f *createUsers) Create(_ context.Context, u *models.User) error {
	if u.Username == nil {
		return errors.New("Error 1048 (23000): Column 'username' cannot be null")
	}
	f.created = u
	return nil
}

func createDeps(users *createUsers, agent AgentCaller) Deps {
	return Deps{Users: users, Agent: agent, BcryptCost: 4}
}

// GH #1938: creating an admin failed with a bare 500. Admins were created
// without a username, and users.username has been NOT NULL since migration
// 000164 (username is the login identifier, ADR-0119).
func TestCreate_AdminGetsAUsernameAndNoLinuxAccount(t *testing.T) {
	name := "ops"
	users := &createUsers{}
	agent := &recordingAgent{}

	res, err := Create(context.Background(), createDeps(users, agent), CreateInput{
		Email: "ops@example.com", Password: "Str0ng-pass", Username: &name, IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if users.created == nil || users.created.Username == nil || *users.created.Username != "ops" || !users.created.IsAdmin {
		t.Fatalf("stored %+v, want admin with username ops", users.created)
	}
	if res.User.Username == nil || *res.User.Username != "ops" {
		t.Fatalf("returned %+v, want username ops", res.User)
	}
	for _, c := range agent.calls {
		if c.method == "user.create" {
			t.Fatal("an admin must not get a Linux account")
		}
	}
}

func TestCreate_AdminUsernameIsDerivedOrValidatedLikeATenants(t *testing.T) {
	users := &createUsers{}
	if _, err := Create(context.Background(), createDeps(users, nil), CreateInput{
		Email: "ops_team@example.com", Password: "Str0ng-pass", IsAdmin: true,
	}); err != nil {
		t.Fatalf("create admin without a username: %v", err)
	}
	if users.created == nil || users.created.Username == nil || *users.created.Username != "ops_team" {
		t.Fatalf("stored %+v, want the username derived from the email", users.created)
	}

	bad := "Not Valid"
	users = &createUsers{}
	_, err := Create(context.Background(), createDeps(users, nil), CreateInput{
		Email: "ops@example.com", Password: "Str0ng-pass", Username: &bad, IsAdmin: true,
	})
	if !errors.Is(err, ErrInvalidUsername) || users.created != nil {
		t.Fatalf("err = %v, stored %+v; want ErrInvalidUsername and no row", err, users.created)
	}
}

// A tenant still gets its Linux account.
func TestCreate_TenantStillGetsALinuxAccount(t *testing.T) {
	name := "shop"
	users := &createUsers{}
	agent := &recordingAgent{}
	if _, err := Create(context.Background(), createDeps(users, agent), CreateInput{
		Email: "shop@example.com", Password: "Str0ng-pass", Username: &name,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	var provisioned bool
	for _, c := range agent.calls {
		if c.method == "user.create" {
			provisioned = true
		}
	}
	if !provisioned {
		t.Fatal("a tenant must get its Linux account")
	}
}
