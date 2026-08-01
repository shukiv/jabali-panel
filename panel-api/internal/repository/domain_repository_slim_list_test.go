package repository_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newCaptureDB is like newMockDB but records every SQL statement the
// repository issues, so tests can assert on the generated column list
// (something the regexp matcher alone can't express negatively).
func newCaptureDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB, *[]string) {
	t.Helper()

	captured := &[]string{}
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		*captured = append(*captured, actualSQL)
		return nil
	})

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)

	mock.ExpectQuery("").
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))

	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      raw,
		SkipInitializeWithVersion: false,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	return gdb, mock, raw, captured
}

// findQuery returns the captured SELECT that reads the domains rows (the
// one carrying an ORDER BY — the count query has none).
func findQuery(t *testing.T, captured []string) string {
	t.Helper()
	for _, q := range captured {
		if strings.Contains(q, "ORDER BY") {
			return q
		}
	}
	t.Fatalf("no domains SELECT with ORDER BY captured; got: %v", captured)
	return ""
}

func expectListQueries(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	mock.ExpectQuery("").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
}

func TestDomainListOmitHeavyColumns(t *testing.T) {
	heavy := []string{"google_dkim", "dkim_public_key", "disclaimer_text", "nginx_safe_options"}

	t.Run("flag set skips heavy columns", func(t *testing.T) {
		db, mock, raw, captured := newCaptureDB(t)
		defer raw.Close()
		repo := repository.NewDomainRepository(db)

		expectListQueries(mock)
		_, _, err := repo.List(context.Background(), repository.ListOptions{Limit: 50, OmitHeavyColumns: true})
		require.NoError(t, err)

		q := findQuery(t, *captured)
		require.Contains(t, q, "`domains`.`name`", "omit mode must expand to an explicit column list")
		for _, col := range heavy {
			require.NotContains(t, q, col, "heavy column %s must not be selected", col)
		}
	})

	t.Run("default keeps full rows", func(t *testing.T) {
		db, mock, raw, captured := newCaptureDB(t)
		defer raw.Close()
		repo := repository.NewDomainRepository(db)

		expectListQueries(mock)
		_, _, err := repo.List(context.Background(), repository.ListOptions{Limit: 50})
		require.NoError(t, err)

		q := findQuery(t, *captured)
		// Without the flag every column — heavy ones included — must stay in
		// the SELECT so the reconciler and other callers get full rows.
		for _, col := range heavy {
			require.Contains(t, q, col, "default mode must select heavy column %s", col)
		}
	})

	t.Run("flag set on ListByUserID", func(t *testing.T) {
		db, mock, raw, captured := newCaptureDB(t)
		defer raw.Close()
		repo := repository.NewDomainRepository(db)

		expectListQueries(mock)
		_, _, err := repo.ListByUserID(context.Background(), "01HZUSER0000000000000001AA", repository.ListOptions{Limit: 50, OmitHeavyColumns: true})
		require.NoError(t, err)

		q := findQuery(t, *captured)
		for _, col := range heavy {
			require.NotContains(t, q, col, "heavy column %s must not be selected", col)
		}
	})
}
