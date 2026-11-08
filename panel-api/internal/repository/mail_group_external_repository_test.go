package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// GH #1833: a distribution list's outside addresses live in
// mail_group_external_members.

func TestMailGroup_ListExternalMembers(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("SELECT .address. FROM .mail_group_external_members. WHERE group_id = . ORDER BY address ASC").
		WithArgs("grp1").
		WillReturnRows(sqlmock.NewRows([]string{"address"}).AddRow("a@gmail.com").AddRow("b@outlook.com"))

	got, err := repo.ListExternalMembers(context.Background(), "grp1")
	require.NoError(t, err)
	require.Equal(t, []string{"a@gmail.com", "b@outlook.com"}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A list with no outside addresses answers an empty slice, never nil, so the
// detail response carries [] and not null.
func TestMailGroup_ListExternalMembers_EmptyIsNotNil(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("FROM .mail_group_external_members.").
		WithArgs("grp1").
		WillReturnRows(sqlmock.NewRows([]string{"address"}))

	got, err := repo.ListExternalMembers(context.Background(), "grp1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestMailGroup_SetExternalMembers_DiffAndInsert(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM .mail_group_external_members. WHERE group_id = . AND address NOT IN").
		WithArgs("grp1", "a@gmail.com", "b@outlook.com").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO .mail_group_external_members.").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	require.NoError(t, repo.SetExternalMembers(context.Background(), "grp1", []string{"a@gmail.com", "b@outlook.com"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_SetExternalMembers_EmptyClearsAll(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM .mail_group_external_members. WHERE group_id = .").
		WithArgs("grp1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	require.NoError(t, repo.SetExternalMembers(context.Background(), "grp1", nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_ListAllExternalMembers(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("SELECT group_id, address AS email FROM .mail_group_external_members. ORDER BY group_id ASC, address ASC").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "email"}).
			AddRow("grp1", "a@gmail.com").AddRow("grp2", "b@outlook.com"))

	got, err := repo.ListAllExternalMembers(context.Background())
	require.NoError(t, err)
	require.Equal(t, []MailGroupMemberEmail{{GroupID: "grp1", Email: "a@gmail.com"}, {GroupID: "grp2", Email: "b@outlook.com"}}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The list views show how many outside addresses each list holds.
func TestMailGroup_ListByDomainID_HasExternalMemberCount(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("FROM mail_group_external_members x WHERE x.group_id = g.id.*AS external_member_count.*FROM mail_groups g").
		WithArgs("dom1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "member_count", "external_member_count"}).
			AddRow("grp1", 2, 3))

	rows, err := repo.ListByDomainID(context.Background(), "dom1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(3), rows[0].ExternalMemberCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailGroup_ListAllWithDomain_HasExternalMemberCount(t *testing.T) {
	db, mock, raw := newMockGroupDB(t)
	defer raw.Close()
	repo := NewMailGroupRepository(db)

	mock.ExpectQuery("AS external_member_count.*FROM mail_groups g JOIN domains d").
		WillReturnRows(sqlmock.NewRows([]string{"id", "external_member_count"}).AddRow("grp1", 4))

	rows, err := repo.ListAllWithDomain(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(4), rows[0].ExternalMemberCount)
	require.NoError(t, mock.ExpectationsWereMet())
}
