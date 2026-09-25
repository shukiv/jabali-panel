package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newMockGroupDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return gdb, mock, raw
}

func TestMailGroup_ExistsByDomainAndLocalPart(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("SELECT count.* FROM .mail_groups. WHERE domain_id = . AND local_part = .").
		WithArgs("dom1", "marketing").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	exists, err := repo.ExistsByDomainAndLocalPart(context.Background(), "dom1", "marketing")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_ListByDomainID_HasMemberCount(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	// The member_count correlated subquery must be present in the SELECT.
	mock.ExpectQuery("member_count.*FROM mail_groups g.*WHERE g.domain_id =").
		WithArgs("dom1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "domain_id", "local_part", "email_cached", "member_count"}).
			AddRow("grp1", "dom1", "marketing", "marketing@example.com", 3))

	rows, err := repo.ListByDomainID(context.Background(), "dom1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(3), rows[0].MemberCount)
	require.Equal(t, "marketing@example.com", rows[0].EmailCached)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_Create(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	now := time.Now()
	g := &models.MailGroup{
		ID: "grp1", DomainID: "dom1", LocalPart: "marketing",
		DisplayName: "Marketing", Description: "Team",
		GroupKind: "resource",
		HasMailbox: true, HasCalendar: true, HasAddressbook: true, HasFiles: true,
		CreatedAt: now, UpdatedAt: now,
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO .mail_groups.").
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), // +internal_only (GH #348)
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), g))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_SetMembers_DiffAndInsert(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectBegin()
	// Drop edges not in the desired set.
	mock.ExpectExec("DELETE FROM .mail_group_members. WHERE group_id = . AND mailbox_id NOT IN").
		WithArgs("grp1", "mb1", "mb2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Insert desired set with ON CONFLICT DO NOTHING.
	mock.ExpectExec("INSERT INTO .mail_group_members.").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	require.NoError(t, repo.SetMembers(context.Background(), "grp1", []string{"mb1", "mb2"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_SetMembers_EmptyClearsAll(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM .mail_group_members. WHERE group_id = .").
		WithArgs("grp1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()

	require.NoError(t, repo.SetMembers(context.Background(), "grp1", nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_Delete_NotFound(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM .mail_groups. WHERE id = .").
		WithArgs("missing").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #1818: a distribution list only fans out to members that can receive
// mail — a disabled or send-only member must not be a list recipient.
func TestMailGroup_ListDeliverableMemberEmails_ExcludesDisabledAndSendOnly(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("SELECT .mb...email_cached. FROM mail_group_members m JOIN mailboxes mb ON mb.id = m.mailbox_id " +
		"WHERE m.group_id = . AND mb.is_disabled = . AND mb.send_only = . ORDER BY mb.email_cached ASC").
		WithArgs("grp1", false, false).
		WillReturnRows(sqlmock.NewRows([]string{"email_cached"}).AddRow("alice@example.com"))

	got, err := repo.ListDeliverableMemberEmails(context.Background(), "grp1")
	require.NoError(t, err)
	require.Equal(t, []string{"alice@example.com"}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_ListAllDeliverableMembers(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("SELECT m.group_id, mb.email_cached AS email FROM mail_group_members m JOIN mailboxes mb ON mb.id = m.mailbox_id " +
		"WHERE mb.is_disabled = . AND mb.send_only = . ORDER BY m.group_id ASC, mb.email_cached ASC").
		WithArgs(false, false).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "email"}).
			AddRow("grp1", "alice@example.com").AddRow("grp2", "bob@example.com"))

	got, err := repo.ListAllDeliverableMembers(context.Background())
	require.NoError(t, err)
	require.Equal(t, []MailGroupMemberEmail{{GroupID: "grp1", Email: "alice@example.com"}, {GroupID: "grp2", Email: "bob@example.com"}}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
