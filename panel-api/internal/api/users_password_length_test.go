package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
)

// bcrypt hashes at most 72 bytes. A longer password is the caller's input and
// gets a 400 that says so, on every route that sets one.
var overLongPasswords = map[string]string{
	"73 ASCII bytes":        strings.Repeat("p", 73),
	"25 letters of 3 bytes": strings.Repeat("日", 25),
	"200 ASCII bytes":       strings.Repeat("p", 200),
}

func requirePasswordTooLong(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "password_too_long", name)
}

func TestUsers_Create_PasswordOverBcryptLimit400(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})

	for name, pw := range overLongPasswords {
		rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
			"email":    "new@example.com",
			"username": "newuser",
			"password": pw,
		})
		requirePasswordTooLong(t, rec, name)
	}
	_, err := repo.FindByEmail(t.Context(), "new@example.com")
	assert.Error(t, err, "no user row was created")

	// 72 bytes is still a valid password.
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "ok@example.com",
		"username": "okuser",
		"password": strings.Repeat("p", 72),
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestUsers_Patch_OwnPasswordOverBcryptLimit400(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)
	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})

	for name, pw := range overLongPasswords {
		rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
			"password":         pw,
			"current_password": "password01",
		})
		requirePasswordTooLong(t, rec, name)
	}
}

// A 400 means nothing was applied: a field sent alongside an over-long
// password is not saved either.
func TestUsers_Patch_PasswordOverBcryptLimitAppliesNothing(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	u := makeUser(t, "u@example.com", false, "password01")
	u.NameFirst = "Before"
	repo.seed(u)
	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})

	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"name_first": "After",
		"email":      "changed@example.com",
		"password":   strings.Repeat("p", 80),
	})
	requirePasswordTooLong(t, rec, "80 bytes with other fields")
	got, err := repo.FindByID(t.Context(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Before", got.NameFirst)
	assert.Equal(t, "u@example.com", got.Email)
}

func TestUsers_Reprovision_PasswordOverBcryptLimit400(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	tenant := makeUser(t, "t@example.com", false, "password01")
	username := "tenant1"
	tenant.Username = &username
	repo.seed(tenant)
	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})

	for name, pw := range overLongPasswords {
		rec := doJSON(t, r, http.MethodPost, "/api/v1/users/"+tenant.ID+"/reprovision", map[string]any{
			"password": pw,
		})
		requirePasswordTooLong(t, rec, name)
	}
}
