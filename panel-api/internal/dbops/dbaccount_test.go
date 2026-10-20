package dbops

import (
	"context"
	"errors"
	"testing"
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
