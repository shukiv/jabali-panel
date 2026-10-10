package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// GH #2004: every panel Postgres database is listed, with the user its
// objects belong to: the Postgres user granted on it first. Only a user of
// the database's own account counts, so its objects never go to another
// account. A database with none maps to "".
func TestPGDatabaseOwners_FirstGrantedPostgresUser(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewPGDatabaseOwnerRepository(db)

	mock.ExpectQuery("(?s)SELECT d.name AS db_name, du.username AS role FROM `databases` AS d "+
		"LEFT JOIN database_user_grants g ON g.database_id = d.id "+
		"LEFT JOIN database_users du ON du.id = g.database_user_id AND du.engine = \\? AND du.user_id = d.user_id "+
		"WHERE d.engine = \\? ORDER BY d.name ASC, g.created_at ASC, g.id ASC").
		WithArgs("postgres", "postgres").
		WillReturnRows(sqlmock.NewRows([]string{"db_name", "role"}).
			AddRow("alice_shop", nil). // a grant to a MariaDB user
			AddRow("alice_shop", "alice_app").
			AddRow("alice_shop", "alice_ro").
			AddRow("bob_blog", nil))

	out, err := repo.ListPGDatabaseOwners(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]string{"alice_shop": "alice_app", "bob_blog": ""}, out)
	require.NoError(t, mock.ExpectationsWereMet())
}
