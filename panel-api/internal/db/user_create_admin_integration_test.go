//go:build integration

// Integration test for GH #1938: creating an admin through userops.Create
// (the admin UI's POST /users) against the real users table. Migration 000164
// made users.username NOT NULL (username is the login identifier, ADR-0119),
// and admins used to be inserted without one, so every admin create failed.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run UserCreateAdmin

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

func TestIntegration_UserCreateAdmin(t *testing.T) {
	dsn := testDSN(t)
	resetSchema(t, dsn)

	gdb, err := db.Open(db.Options{DSN: dsn, Silent: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, _ := gdb.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	ctx := context.Background()
	users := repository.NewUserRepository(gdb)
	deps := userops.Deps{Users: users, BcryptCost: 4}

	name := "ops1938"
	res, err := userops.Create(ctx, deps, userops.CreateInput{
		Email: "ops@example.com", Password: "Str0ng-pass-1938", Username: &name, IsAdmin: true,
	})
	require.NoError(t, err)
	got, err := users.FindByID(ctx, res.User.ID)
	require.NoError(t, err)
	require.True(t, got.IsAdmin)
	require.NotNil(t, got.Username)
	require.Equal(t, "ops1938", *got.Username)

	// The username is shared with tenants: a second account cannot take it.
	_, err = userops.Create(ctx, deps, userops.CreateInput{
		Email: "other@example.com", Password: "Str0ng-pass-1938", Username: &name, IsAdmin: true,
	})
	require.True(t, errors.Is(err, userops.ErrUsernameTaken), "want ErrUsernameTaken, got %v", err)
}
