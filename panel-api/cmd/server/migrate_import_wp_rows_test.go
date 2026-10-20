package main

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type wpMigUsers struct {
	repository.DatabaseUserRepository
	rows []models.DatabaseUser
}

func (r *wpMigUsers) Create(_ context.Context, du *models.DatabaseUser) error {
	r.rows = append(r.rows, *du)
	return nil
}

type wpMigGrants struct {
	repository.DatabaseUserGrantRepository
	rows []models.DatabaseUserGrant
}

func (r *wpMigGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	r.rows = append(r.rows, *g)
	return nil
}

// The WordPress SSH import's database user is one of the target's database
// users, granted on the database it made: the panel shows it, can rotate its
// password and drops it with the account.
func TestRecordWPMigDBUser_RecordsTheUserAndItsGrant(t *testing.T) {
	users, grants := &wpMigUsers{}, &wpMigGrants{}
	if err := recordWPMigDBUser(context.Background(), users, grants, "u-alice", "db-1", "alice_wpm1a2b3c", "pw-0123456789"); err != nil {
		t.Fatalf("recordWPMigDBUser: %v", err)
	}
	if len(users.rows) != 1 {
		t.Fatalf("rows %+v, want one", users.rows)
	}
	du := users.rows[0]
	if du.UserID != "u-alice" || du.Username != "alice_wpm1a2b3c" || du.Engine != "mariadb" {
		t.Errorf("row %+v", du)
	}
	if bcrypt.CompareHashAndPassword([]byte(du.PasswordHash), []byte("pw-0123456789")) != nil {
		t.Error("row hash does not match the user's password")
	}
	if len(grants.rows) != 1 || grants.rows[0].DatabaseID != "db-1" || grants.rows[0].DatabaseUserID != du.ID ||
		grants.rows[0].GrantLevel != "rw" || grants.rows[0].Privileges != "ALL" {
		t.Errorf("grant rows %+v, want one rw ALL on db-1", grants.rows)
	}
}

// With no database row to grant on, the user is still recorded.
func TestRecordWPMigDBUser_NoDatabaseRowNoGrant(t *testing.T) {
	users, grants := &wpMigUsers{}, &wpMigGrants{}
	if err := recordWPMigDBUser(context.Background(), users, grants, "u-alice", "", "alice_wpm1a2b3c", "pw-0123456789"); err != nil {
		t.Fatalf("recordWPMigDBUser: %v", err)
	}
	if len(users.rows) != 1 || len(grants.rows) != 0 {
		t.Errorf("rows %+v grants %+v", users.rows, grants.rows)
	}
}
