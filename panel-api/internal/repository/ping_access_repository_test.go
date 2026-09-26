package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// Only users on a package that allows ICMP are listed. The INNER JOIN and the
// egress_icmp filter are the deny path for a NULL package and a package
// without ping. Falsify by switching INNER→LEFT or dropping the filter.
func TestPingAccess_ListsOnlyPackagesThatAllowPing(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewPingAccessRepository(db)

	mock.ExpectQuery("(?s)SELECT `u`.`username` FROM users AS u INNER JOIN hosting_packages hp ON hp.id = u.package_id WHERE hp.egress_icmp = \\? AND \\(u.username IS NOT NULL AND u.username <> ''\\) ORDER BY u.username ASC").
		WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"username"}).AddRow("alice").AddRow("bob"))

	out, err := repo.ListPingAllowedUsernames(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"alice", "bob"}, out)
	require.NoError(t, mock.ExpectationsWereMet())
}
