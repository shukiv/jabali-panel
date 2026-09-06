package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// isn't the bundle's own (home is name-keyed), a bundle with no email, and an
// email already in use — without ever creating an account.
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
		if u, ok := h.createUserFromBundle(c, "/staged.tar.zst", target, nil); ok || u != nil {
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
		{"no email", map[string]any{"username": "alice", "email": ""}, "alice", false, http.StatusBadRequest},
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
