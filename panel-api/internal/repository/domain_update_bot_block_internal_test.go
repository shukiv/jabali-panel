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

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Domain.Update writes only its allowlisted columns, so a flag missing from
// the list is dropped without an error. block_ai_crawlers must be on it, and
// the request limits, which have their own writer, must not.
func TestDomainRepository_UpdateWritesBlockAICrawlers(t *testing.T) {
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
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	d := &models.Domain{ID: "d1", Name: "example.com", BlockAICrawlers: true, RateLimitRPS: 9}
	require.NoError(t, NewDomainRepository(db).Update(context.Background(), d))

	var q string
	for _, s := range sent {
		if strings.HasPrefix(s, "UPDATE") {
			q = s
		}
	}
	require.Contains(t, q, "`block_ai_crawlers`=", "Update drops block_ai_crawlers: %s", q)
	require.NotContains(t, q, "rate_limit_rps", "request limits are written by SetRateLimits only")
}
