package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newCountsMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT VERSION\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return gdb, mock, raw
}

// JAB-148: /admin/counts must issue ONE round-trip (a single SELECT with three
// scalar COUNT subqueries) and scan the aliased columns straight into the
// response — not silently return zeros.
func TestAdminCounts_SingleQueryScans(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, raw := newCountsMockDB(t)
	defer raw.Close()

	mock.ExpectQuery(`SELECT .*\(SELECT COUNT\(\*\) FROM users\) AS users.*\(SELECT COUNT\(\*\) FROM domains\) AS domains.*\(SELECT COUNT\(\*\) FROM mailboxes\) AS mailboxes`).
		WillReturnRows(sqlmock.NewRows([]string{"users", "domains", "mailboxes"}).
			AddRow(int64(7), int64(4), int64(19)))

	h := &adminCountsHandler{cfg: AdminCountsHandlerConfig{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/counts", nil)

	h.get(c)

	require.Equal(t, http.StatusOK, w.Code)
	var resp adminCountsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, int64(7), resp.Users)
	require.Equal(t, int64(4), resp.Domains)
	require.Equal(t, int64(19), resp.Mailboxes)
	require.NoError(t, mock.ExpectationsWereMet())
}
