package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// JAB-279: the preview-slug collision check reads every preview-enabled domain
// through ListPreviewEnabled. The query must load only the columns the check
// needs, filter to preview-enabled rows, and never page (a LIMIT would let a
// collision past the page through).
func TestDomainRepository_ListPreviewEnabled_NarrowUnpagedQuery(t *testing.T) {
	var sent []string
	matcher := sqlmock.QueryMatcherFunc(func(_, actual string) error {
		sent = append(sent, actual)
		return nil
	})
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	defer raw.Close()

	mock.ExpectQuery("version").
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	mock.ExpectQuery("preview").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "temp_url_enabled"}).
			AddRow("d1", "my-site.com", true).
			AddRow("d2", "shop.example.com", true))

	rows, err := NewDomainRepository(db).ListPreviewEnabled(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "my-site.com", rows[0].Name)
	require.True(t, rows[0].TempURLEnabled)
	require.NoError(t, mock.ExpectationsWereMet())

	var q string
	for _, s := range sent {
		if strings.Contains(s, "FROM `domains`") {
			q = s
		}
	}
	require.NotEmpty(t, q, "no domains query was sent: %q", sent)

	// Only the select list (before FROM) names the loaded columns; the WHERE
	// clause names temp_url_enabled too.
	selectList := strings.ToLower(q[:strings.Index(q, " FROM ")])
	for _, col := range []string{"`id`", "`name`", "`temp_url_enabled`"} {
		require.Contains(t, selectList, col)
	}
	for _, heavy := range []string{"*", "dkim", "nginx", "disclaimer", "doc_root", "user_id"} {
		require.NotContains(t, selectList, heavy, "the preview query must not load %s", heavy)
	}
	lower := strings.ToLower(q)
	require.Contains(t, lower, "temp_url_enabled = ", "the query must filter to preview-enabled domains")
	require.NotContains(t, lower, " limit ", "the query must not page")
	require.NotContains(t, lower, "join", "the query needs no join")
}
