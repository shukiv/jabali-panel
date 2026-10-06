package userops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// adoptUsers is createUsers plus the link and rollback writes Create makes
// when Kratos is wired.
type adoptUsers struct {
	createUsers
	linked  string
	deleted bool
	// owners maps a Kratos identity id to the panel user linked to it.
	owners map[string]*models.User
}

func (f *adoptUsers) FindByKratosIdentityID(_ context.Context, identityID string) (*models.User, error) {
	if u, ok := f.owners[identityID]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

func (f *adoptUsers) LinkKratosIdentity(_ context.Context, _ string, identityID string) error {
	f.linked = identityID
	return nil
}
func (f *adoptUsers) Delete(context.Context, string) error {
	f.deleted = true
	return nil
}

// fakeKratosAdmin answers like Kratos when an identity for the email already
// exists: create → 409, list → that identity, PATCH → records the patch.
type fakeKratosAdmin struct {
	mu        sync.Mutex
	patchCode int
	patches   map[string]string // identity id → patch body
}

func (k *fakeKratosAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/admin/identities":
		w.WriteHeader(http.StatusConflict)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/identities":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "existing-1", "traits": map[string]any{"email": "carol@example.com"}}})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/admin/identities/"):
		b, _ := io.ReadAll(r.Body)
		k.patches[strings.TrimPrefix(r.URL.Path, "/admin/identities/")] = string(b)
		code := k.patchCode
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"id":"existing-1"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func adoptCreate(t *testing.T, k *fakeKratosAdmin, owners ...map[string]*models.User) (*adoptUsers, *CreateResult, error) {
	t.Helper()
	srv := httptest.NewServer(k)
	t.Cleanup(srv.Close)
	users := &adoptUsers{}
	if len(owners) > 0 {
		users.owners = owners[0]
	}
	name := "carol"
	res, err := Create(context.Background(), Deps{
		Users: users, BcryptCost: 4, KratosClient: kratosclient.NewClient(srv.URL, srv.URL),
	}, CreateInput{Email: "carol@example.com", Password: "Str0ng-pass-1", Username: &name, IsAdmin: true})
	return users, res, err
}

// When Kratos already has an identity for the new account's email, Create
// reuses it (idempotent reruns). It used to keep that identity's password, so
// the password just set for the account didn't sign in, and whoever knew the
// old one did. The adopted identity now gets the new account's password.
func TestCreate_AdoptedIdentityGetsTheNewPassword(t *testing.T) {
	k := &fakeKratosAdmin{patches: map[string]string{}}
	users, _, err := adoptCreate(t, k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if users.linked != "existing-1" {
		t.Fatalf("linked %q, want the existing identity", users.linked)
	}
	var created *models.User = users.created
	patch, ok := k.patches["existing-1"]
	if !ok {
		t.Fatal("the adopted identity's password was not replaced")
	}
	if !strings.Contains(patch, "/credentials/password/config/hashed_password") || !strings.Contains(patch, created.PasswordHash) {
		t.Fatalf("patch %s should set the new account's hash", patch)
	}
}

// If the password can't be replaced, the account isn't created: it would be
// linked to an identity whose password nobody chose for it.
func TestCreate_AdoptedIdentityPasswordFailureRollsBack(t *testing.T) {
	k := &fakeKratosAdmin{patches: map[string]string{}, patchCode: http.StatusInternalServerError}
	users, _, err := adoptCreate(t, k)
	if err == nil {
		t.Fatal("create succeeded although the adopted identity kept its old password")
	}
	if !users.deleted || users.linked != "" {
		t.Fatalf("deleted=%v linked=%q, want the panel row rolled back and nothing linked", users.deleted, users.linked)
	}
}

// The identity found by email can be another panel account's (its panel
// email changed, its Kratos traits didn't). Taking it over would set that
// account's password: the create fails without touching the identity.
func TestCreate_IdentityLinkedToAnotherAccountIsNotTouched(t *testing.T) {
	k := &fakeKratosAdmin{patches: map[string]string{}}
	users, _, err := adoptCreate(t, k, map[string]*models.User{"existing-1": {ID: "u-other"}})
	if err == nil {
		t.Fatal("create succeeded on another account's sign-in identity")
	}
	if len(k.patches) != 0 {
		t.Fatalf("the other account's identity was changed: %v", k.patches)
	}
	if !users.deleted || users.linked != "" {
		t.Fatalf("deleted=%v linked=%q, want the panel row rolled back and nothing linked", users.deleted, users.linked)
	}
}
