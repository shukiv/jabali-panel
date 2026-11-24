package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
)

func TestBootstrapAdmin_CreatesWhenMissing(t *testing.T) {
	t.Parallel()
	users := newFakeUserRepo()

	res, err := auth.BootstrapAdmin(context.Background(), users, auth.BootstrapOptions{
		Email: "admin@example.com", Password: "strongpass", BcryptCost: testCost,
	})
	require.NoError(t, err)
	assert.True(t, res.Created)

	// User was actually inserted as admin with hashed password.
	u, err := users.FindByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	assert.True(t, u.IsAdmin)
	assert.True(t, auth.VerifyPassword(u.PasswordHash, "strongpass"))
}

// TestBootstrapAdmin_Username: the installer's admin username is the login
// username; without one, or with an invalid one, it comes from the email.
func TestBootstrapAdmin_Username(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ given, email, want string }{
		{"", "admin@example.com", "admin"},
		{"", "Shuki.V@example.com", "shuki_v"},
		{"ops_lead", "admin@example.com", "ops_lead"},
		{"Bad Name", "admin@example.com", "admin"},
		{"9lives", "boss@example.com", "boss"},
		{"averyveryveryveryveryverylongname1", "admin@example.com", "admin"},
	} {
		users := newFakeUserRepo()
		res, err := auth.BootstrapAdmin(context.Background(), users, auth.BootstrapOptions{
			Email: tc.email, Username: tc.given, Password: "strongpass", BcryptCost: testCost,
		})
		require.NoError(t, err)
		require.True(t, res.Created)
		u, err := users.FindByEmail(context.Background(), tc.email)
		require.NoError(t, err)
		require.NotNil(t, u.Username)
		assert.Equal(t, tc.want, *u.Username, "given %q email %q", tc.given, tc.email)
	}
}

func TestBootstrapAdmin_SkipsWhenEnvEmpty(t *testing.T) {
	t.Parallel()
	users := newFakeUserRepo()

	res, err := auth.BootstrapAdmin(context.Background(), users, auth.BootstrapOptions{
		Email: "", Password: "", BcryptCost: testCost,
	})
	require.NoError(t, err)
	assert.True(t, res.SkippedEmpty)
	assert.False(t, res.Created)
}

func TestBootstrapAdmin_IdempotentWhenAlreadyAdmin(t *testing.T) {
	t.Parallel()
	users := newFakeUserRepo()
	seedUser(t, users, "admin@example.com", "originalpw", true)

	// Re-running with a different password should NOT overwrite — safety
	// against a leaked env var blowing away the operator credential.
	res, err := auth.BootstrapAdmin(context.Background(), users, auth.BootstrapOptions{
		Email: "admin@example.com", Password: "newpw-from-env", BcryptCost: testCost,
	})
	require.NoError(t, err)
	assert.False(t, res.Created)
	assert.NotEmpty(t, res.ExistingID)

	u, err := users.FindByEmail(context.Background(), "admin@example.com")
	require.NoError(t, err)
	assert.True(t, auth.VerifyPassword(u.PasswordHash, "originalpw"), "original password must still work")
	assert.False(t, auth.VerifyPassword(u.PasswordHash, "newpw-from-env"))
}

func TestBootstrapAdmin_RefusesToElevateExistingNonAdmin(t *testing.T) {
	t.Parallel()
	users := newFakeUserRepo()
	seedUser(t, users, "alice@example.com", "hunter2", false)

	_, err := auth.BootstrapAdmin(context.Background(), users, auth.BootstrapOptions{
		Email: "alice@example.com", Password: "upgrade", BcryptCost: testCost,
	})
	require.Error(t, err, "must refuse silent privilege escalation")
}
