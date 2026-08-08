package repository

// Regression for feedback_gorm_default1_bool_zero_value: the four MailGroup
// feature flags carried gorm `default:1`, so GORM substituted the DB default
// (true) for an intentional false on INSERT. A mail group created with a
// feature UNCHECKED landed with it enabled. This asserts the emitted INSERT
// binds the actual struct values — false stays false.

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestMailGroup_Create_PersistsFalseFeatureFlags(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer raw.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	repo := NewMailGroupRepository(gdb)

	mock.ExpectBegin()
	// Column order: id,domain_id,local_part,email_cached,display_name,
	// description,group_kind,has_mailbox,has_calendar,has_addressbook,has_files,
	// internal_only,created_at,updated_at. The four has_* MUST bind false — with
	// the old `default:1` tag GORM bound true here and this fails.
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `mail_groups`")).
		WithArgs(
			"g1", "d1", "team", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			false, false, false, false, // has_mailbox, has_calendar, has_addressbook, has_files
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	g := &models.MailGroup{
		ID: "g1", DomainID: "d1", LocalPart: "team",
		HasMailbox: false, HasCalendar: false, HasAddressbook: false, HasFiles: false,
	}
	require.NoError(t, repo.Create(context.Background(), g))
	require.NoError(t, mock.ExpectationsWereMet(),
		"a MailGroup created with feature flags false must INSERT them as false (gorm default:1 scar)")
}
