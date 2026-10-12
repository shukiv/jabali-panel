package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// guardUsersRepo answers FindByEmail (used by the create guard) and nothing
// else — the guards under test all return before any other repo call.
type guardUsersRepo struct {
	repository.UserRepository
	emailTaken bool
}

func (g *guardUsersRepo) FindByEmail(context.Context, string) (*models.User, error) {
	if g.emailTaken {
		return &models.User{ID: "existing"}, nil
	}
	return nil, repository.ErrNotFound
}

// stubPackages is a non-nil PackageRepository so createUserFromBundle doesn't
// early-return "unavailable"; its methods are never reached by the guards.
type stubPackages struct{ repository.PackageRepository }

// GH #1408: create-from-manifest is fed an attacker-controllable bundle. The
// guards BEFORE userops.Create must reject an admin bundle, a username that
// isn't the bundle's own (home is name-keyed), and an email already in use —
// without ever creating an account. A bundle with no email is not refused
// (GH #1993): an account's email is optional.
func TestCreateUserFromBundle_Guards(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mk := func(user map[string]any, emailTaken bool) *backupHandler {
		agent := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
			if cmd != "backup.inspect_uploaded_tar" {
				t.Fatalf("guard must only inspect, got agent call %q", cmd)
			}
			b, _ := json.Marshal(map[string]any{"user": user})
			return b, nil
		}}
		return &backupHandler{cfg: BackupHandlerConfig{
			Agent:    agent,
			Users:    &guardUsersRepo{emailTaken: emailTaken},
			Packages: stubPackages{},
		}}
	}
	call := func(h *backupHandler, target string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
		ins, _, _ := h.inspectUpload(c.Request.Context(), "/staged.tar.zst")
		if u, ok := h.createUserFromBundle(c, ins, target, nil); ok || u != nil {
			t.Fatalf("guard must not create a user (ok=%v)", ok)
		}
		return w.Code
	}

	cases := []struct {
		name   string
		user   map[string]any
		target string
		taken  bool
		want   int
	}{
		{"admin bundle refused", map[string]any{"username": "alice", "email": "a@x.com", "is_admin": true}, "alice", false, http.StatusForbidden},
		{"username mismatch", map[string]any{"username": "alice", "email": "a@x.com"}, "bob", false, http.StatusBadRequest},
		{"email already in use", map[string]any{"username": "alice", "email": "a@x.com"}, "alice", true, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(mk(tc.user, tc.taken), tc.target); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// createUsersRepo records the account create-from-bundle makes. FindByEmail
// answers a user for ANY email, "" included: another account with no email
// must not make a no-email bundle look "taken".
type createUsersRepo struct {
	repository.UserRepository
	mu      sync.Mutex
	created []*models.User
}

func (r *createUsersRepo) FindByEmail(context.Context, string) (*models.User, error) {
	return &models.User{ID: "someone-else"}, nil
}

func (r *createUsersRepo) Create(_ context.Context, u *models.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, u)
	return nil
}

func (r *createUsersRepo) Update(context.Context, *models.User) error { return nil }

func (r *createUsersRepo) Delete(context.Context, string) error { return nil }

// createAgent answers the inspect and the account's user.create; the maldet
// reload userops fires in a goroutine gets an empty answer.
type createAgent struct{ user map[string]any }

func (a *createAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	switch cmd {
	case "backup.inspect_uploaded_tar":
		return json.Marshal(map[string]any{"user": a.user})
	case "user.create":
		return json.RawMessage(`{"uid":2001}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// GH #1993: a backup of an account with no email restores into a new account.
// The email is optional for an account (userops.Create, M54/GH#176), so the
// restore no longer refuses the backup.
func TestCreateUserFromBundle_NoEmailCreatesTheAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	users := &createUsersRepo{}
	h := &backupHandler{cfg: BackupHandlerConfig{
		Agent:    &createAgent{user: map[string]any{"username": "alice", "email": ""}},
		Users:    users,
		Packages: stubPackages{},
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	ins, _, err := h.inspectUpload(c.Request.Context(), "/staged.tar.zst")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	u, ok := h.createUserFromBundle(c, ins, "alice", nil)

	if !ok || u == nil {
		t.Fatalf("no account created: status %d, body %s", w.Code, w.Body.String())
	}
	users.mu.Lock()
	defer users.mu.Unlock()
	if len(users.created) != 1 {
		t.Fatalf("created %d accounts, want 1", len(users.created))
	}
	got := users.created[0]
	if got.Username == nil || *got.Username != "alice" || got.Email != "" || got.IsAdmin {
		t.Errorf("created %+v, want non-admin alice with no email", got)
	}
}
