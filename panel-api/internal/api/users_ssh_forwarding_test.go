package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1229 — admin opt-in + tenant read-only for SSH TCP forwarding.

// hostingUserWithSSH seeds a non-admin user with a Linux username on an
// SSH-enabled package, returning the user and package.
func hostingUserWithSSH(t *testing.T, repo *memUserRepo, pkgRepo *memPackageRepo, sshEnabled bool) *models.User {
	t.Helper()
	pkg := &models.HostingPackage{ID: ids.NewULID(), Name: "Shell", SSHEnabled: sshEnabled}
	pkgRepo.seed(pkg)
	u := makeUser(t, "host@example.com", false, "password123")
	uname := "hostuser"
	u.Username = &uname
	u.PackageID = &pkg.ID
	repo.seed(u)
	return u
}

func TestSSHForwarding_AdminEnableDisableRoundTrip(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	pkgRepo := newMemPackageRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	u := hostingUserWithSSH(t, repo, pkgRepo, true)

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})

	// Enable.
	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/users/"+u.ID+"/ssh-forwarding", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		SSHForwardingEnabled bool `json:"ssh_forwarding_enabled"`
		SSHEnabled           bool `json:"ssh_enabled"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.SSHForwardingEnabled)
	assert.True(t, out.SSHEnabled)

	// Persisted on the row (durable flag, not just group membership).
	got, err := repo.FindByID(t.Context(), u.ID)
	require.NoError(t, err)
	assert.True(t, got.SSHForwardingEnabled)

	// Admin GET reflects it.
	rec = doJSON(t, r, http.MethodGet, "/api/v1/admin/users/"+u.ID+"/ssh-forwarding", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.SSHForwardingEnabled)

	// Disable.
	rec = doJSON(t, r, http.MethodPost, "/api/v1/admin/users/"+u.ID+"/ssh-forwarding", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code)
	got, err = repo.FindByID(t.Context(), u.ID)
	require.NoError(t, err)
	assert.False(t, got.SSHForwardingEnabled)
}

func TestSSHForwarding_NonAdminForbidden(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	pkgRepo := newMemPackageRepo()
	u := hostingUserWithSSH(t, repo, pkgRepo, true)

	// The target user themselves must NOT be able to flip the control.
	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: u.ID, Email: u.Email})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/users/"+u.ID+"/ssh-forwarding", map[string]any{"enabled": true})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	got, err := repo.FindByID(t.Context(), u.ID)
	require.NoError(t, err)
	assert.False(t, got.SSHForwardingEnabled, "non-admin POST must not have changed the flag")
}

func TestSSHForwarding_NotAHostingUser422(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	pkgRepo := newMemPackageRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	// Another admin (no Linux username) is not a valid target.
	target := makeUser(t, "admin2@example.com", true, "pw")
	repo.seed(target)

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/users/"+target.ID+"/ssh-forwarding", map[string]any{"enabled": true})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "not_a_hosting_user")
}

func TestSSHForwarding_MeReadOnlyStatus(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	pkgRepo := newMemPackageRepo()
	u := hostingUserWithSSH(t, repo, pkgRepo, true)
	// Pretend an admin already turned it on (seed copies, so set via the repo).
	require.NoError(t, repo.SetSSHForwardingEnabled(t.Context(), u.ID, true))

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: u.ID, Email: u.Email})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/me/ssh-forwarding", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		SSHForwardingEnabled bool `json:"ssh_forwarding_enabled"`
		SSHEnabled           bool `json:"ssh_enabled"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.SSHForwardingEnabled)
	assert.True(t, out.SSHEnabled)
}
