package dbops

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type countRows map[string]int64

func (r countRows) CountByEngineAndUsername(_ context.Context, engine, username string) (int64, error) {
	return r[engine+"|"+username], nil
}

func TestCheckNewAccount(t *testing.T) {
	rows := countRows{"mariadb|alice_x_app": 1, "postgres|alice_x_pg": 1}
	for _, c := range []struct {
		engine, name string
		want         error
	}{
		{"mariadb", "alice_app", nil},
		{"", "alice_x_app", ErrAccountNameTaken}, // "" is MariaDB
		{"mariadb", "alice_x_app", ErrAccountNameTaken},
		{"postgres", "alice_x_app", nil}, // another engine's account
		{"postgres", "alice_x_pg", ErrAccountNameTaken},
		{"mariadb", "alice_x_mysqladmin", ErrAccountNameReserved},
		{"postgres", "alice_x_PGADMIN", ErrAccountNameReserved},
	} {
		if err := CheckNewAccount(context.Background(), rows, c.engine, c.name); !errors.Is(err, c.want) {
			t.Errorf("CheckNewAccount(%q, %q) = %v, want %v", c.engine, c.name, err, c.want)
		}
	}
}

func TestSoleAccountRow(t *testing.T) {
	rows := countRows{"mariadb|alice_app": 1, "mariadb|alice_x_app": 2, "mariadb|bob_mysqladmin": 1}
	for name, want := range map[string]bool{"alice_app": true, "alice_x_app": false, "bob_mysqladmin": false} {
		if got, err := SoleAccountRow(context.Background(), rows, "mariadb", name); err != nil || got != want {
			t.Errorf("SoleAccountRow(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
}

type ownerRows map[string][]string

func (r ownerRows) ListByEngineAndUsername(_ context.Context, engine, username string) ([]models.DatabaseUser, error) {
	var out []models.DatabaseUser
	for i, owner := range r[engine+"|"+username] {
		out = append(out, models.DatabaseUser{ID: fmt.Sprintf("du-%d", i), UserID: owner, Username: username, Engine: engine})
	}
	return out, nil
}

func TestImportedAccount(t *testing.T) {
	rows := ownerRows{
		"mariadb|alice_wp":    {"u-alice"},
		"mariadb|bob_wp":      {"u-bob"},
		"mariadb|alice_x_app": {"u-alice", "u-alice-x"},
		"postgres|alice_pg":   {"u-bob"},
	}
	for _, c := range []struct {
		name         string
		wantExisting bool
		want         error
	}{
		{"notary_45635", false, nil},               // no row: create only
		{"alice_wp", true, nil},                    // alice's own sole row
		{"bob_wp", false, ErrAccountNotOwned},      // another tenant's row
		{"alice_x_app", false, ErrAccountNotOwned}, // two rows, one alice's
		{"alice_pg", false, nil},                   // another engine's account
		{"bob_mysqladmin", false, ErrAccountNameReserved},
		{"alice_PGADMIN", false, ErrAccountNameReserved},
	} {
		row, err := ImportedAccount(context.Background(), rows, "mariadb", c.name, "u-alice")
		if (row != nil) != c.wantExisting || !errors.Is(err, c.want) {
			t.Errorf("ImportedAccount(%q) = %+v, %v; want existing %v, %v", c.name, row, err, c.wantExisting, c.want)
		}
		if row != nil && (row.UserID != "u-alice" || row.Username != c.name) {
			t.Errorf("ImportedAccount(%q) row %+v, want alice's", c.name, row)
		}
	}
}
