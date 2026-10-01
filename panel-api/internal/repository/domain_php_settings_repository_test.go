package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// GH #1701 Slice 2: UpdatePHPSettings writes the three flag columns (a column
// missing here would make the page save look fine and silently drop the value).
func TestDomain_UpdatePHPSettings_WritesTheFlagColumns(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	on, off := true, false
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET .*`php_file_uploads`=\\?.*`php_log_errors`=\\?.*`php_short_open_tag`=\\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := NewDomainRepository(db).UpdatePHPSettings(context.Background(), "d1", DomainPHPSettings{
		LogErrors: &off, FileUploads: &off, ShortOpenTag: &on,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
