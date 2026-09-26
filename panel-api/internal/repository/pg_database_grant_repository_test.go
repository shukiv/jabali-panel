package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// Only Postgres databases and Postgres users are listed, grouped by database.
func TestPGDatabaseGrants_GroupsPostgresGrantsByDatabase(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewPGDatabaseGrantRepository(db)

	mock.ExpectQuery("(?s)SELECT d.name AS db_name, du.username AS role FROM `databases` AS d INNER JOIN database_user_grants g ON g.database_id = d.id INNER JOIN database_users du ON du.id = g.database_user_id WHERE d.engine = \\? AND du.engine = \\? ORDER BY d.name ASC, du.username ASC").
		WithArgs("postgres", "postgres").
		WillReturnRows(sqlmock.NewRows([]string{"db_name", "role"}).
			AddRow("alice_shop", "alice_app").AddRow("alice_shop", "alice_ro").AddRow("bob_blog", "bob_app"))

	out, err := repo.ListPGDatabaseGrants(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"alice_shop": {"alice_app", "alice_ro"}, "bob_blog": {"bob_app"}}, out)
	require.NoError(t, mock.ExpectationsWereMet())
}
