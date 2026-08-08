package repository

// Regression for feedback_gorm_default1_bool_zero_value on the three
// Enabled bools that can be created --disabled: backup_destination,
// backup_schedule, notification_channel. With a gorm `default:1` tag GORM binds
// the DB default (true) for a zero-valued bool on INSERT, so a row created with
// Enabled=false landed enabled — for a backup destination/schedule that means a
// backup the operator disabled silently runs. This asserts the emitted INSERT
// writes enabled=false.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// insertedValue parses a ToSQL INSERT ("INSERT INTO t (a,b,c) VALUES (1,2,3)")
// and returns the inlined value for the named column.
func insertedValue(t *testing.T, sql, column string) string {
	t.Helper()
	m := regexp.MustCompile(`(?i)INSERT INTO\s+\S+\s+\(([^)]*)\)\s+VALUES\s+\((.*)\)`).FindStringSubmatch(sql)
	require.Len(t, m, 3, "unparseable INSERT: %s", sql)
	cols := strings.Split(m[1], ",")
	vals := splitTopLevel(m[2])
	require.Len(t, vals, len(cols), "col/val count mismatch")
	for i, c := range cols {
		if strings.Trim(strings.TrimSpace(c), "`") == column {
			return strings.TrimSpace(vals[i])
		}
	}
	t.Fatalf("column %q not in INSERT: %s", column, sql)
	return ""
}

// splitTopLevel splits a VALUES body on commas that are not inside quotes.
func splitTopLevel(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'':
			inQ = !inQ
			cur.WriteByte(ch)
		case ch == ',' && !inQ:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	out = append(out, cur.String())
	return out
}

func newToSQLDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	mock.ExpectQuery("SELECT VERSION").WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow("10.11.6-MariaDB"))
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

func TestEnabledFalse_PersistsOnCreate(t *testing.T) {
	db := newToSQLDB(t)

	t.Run("backup_destination", func(t *testing.T) {
		sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
			return tx.Create(&models.BackupDestination{ID: "d1", Name: "n", Kind: "local", URL: "/x", Enabled: false})
		})
		require.Equal(t, "false", insertedValue(t, sql, "enabled"),
			"a destination created disabled must INSERT enabled=false (gorm default:1 scar)")
	})

	t.Run("backup_schedule", func(t *testing.T) {
		sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
			return tx.Create(&models.BackupSchedule{ID: "s1", Kind: "account_backup", CronExpr: "0 3 * * *", Enabled: false})
		})
		require.Equal(t, "false", insertedValue(t, sql, "enabled"),
			"a schedule created disabled must INSERT enabled=false")
	})

	t.Run("notification_channel", func(t *testing.T) {
		sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
			return tx.Create(&models.NotificationChannel{ID: "c1", Kind: "webpush", Enabled: false})
		})
		require.Equal(t, "false", insertedValue(t, sql, "enabled"),
			"a channel created disabled must INSERT enabled=false")
	})
}
