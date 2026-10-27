package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestDomainRepository_FindPanelPrimary_Found: returns the is_panel_primary=1 row.
func TestDomainRepository_FindPanelPrimary_Found(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT .* FROM `domains` WHERE is_panel_primary = \\?.*LIMIT").
		WithArgs(true, 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "user_id", "name", "is_panel_primary", "email_enabled", "created_at", "updated_at"},
		).AddRow("dom_1", "usr_1", "panel.example.com", true, true, time.Now(), time.Now()))

	d, err := repo.FindPanelPrimary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, "panel.example.com", d.Name)
	require.True(t, d.IsPanelPrimary)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_FindPanelPrimary_NotFound: returns the typed error,
// NOT gorm.ErrRecordNotFound — callers need to distinguish this from an
// unrelated lookup failure.
func TestDomainRepository_FindPanelPrimary_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT .* FROM `domains` WHERE is_panel_primary = \\?.*LIMIT").
		WithArgs(true, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // empty

	d, err := repo.FindPanelPrimary(context.Background())
	require.Nil(t, d)
	require.ErrorIs(t, err, ErrPanelPrimaryNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_MarkPanelPrimary_ClearsOthersThenSets: the transaction
// body MUST clear any other =1 row before setting the target, otherwise the
// "at most one" invariant breaks. We verify the SQL trio inside a txn.
func TestDomainRepository_MarkPanelPrimary_ClearsOthersThenSets(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectBegin()
	// Verify target exists.
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `domains` WHERE id = \\?").
		WithArgs("dom_target").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// Clear other is_panel_primary=1 rows.
	mock.ExpectExec("UPDATE `domains` SET `is_panel_primary`=\\?,`updated_at`=\\? WHERE is_panel_primary = \\? AND id != \\?").
		WithArgs(false, sqlmock.AnyArg(), true, "dom_target").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Set target.
	mock.ExpectExec("UPDATE `domains` SET `is_panel_primary`=\\?,`updated_at`=\\? WHERE id = \\?").
		WithArgs(true, sqlmock.AnyArg(), "dom_target").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.MarkPanelPrimary(context.Background(), "dom_target")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_MarkPanelPrimary_TargetMissing: returns ErrNotFound
// and does NOT attempt the clear/set updates — avoids silently marking
// nothing.
func TestDomainRepository_MarkPanelPrimary_TargetMissing(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `domains` WHERE id = \\?").
		WithArgs("dom_missing").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()

	err := repo.MarkPanelPrimary(context.Background(), "dom_missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_Delete_RefusesPanelPrimary: the guard must fire BEFORE
// any DELETE statement — verify the mock sees no DELETE expectation exercise.
func TestDomainRepository_Delete_RefusesPanelPrimary(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT `id`,`is_panel_primary` FROM `domains` WHERE id = \\?.*LIMIT").
		WithArgs("dom_panel", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "is_panel_primary"},
		).AddRow("dom_panel", true))

	err := repo.Delete(context.Background(), "dom_panel")
	require.ErrorIs(t, err, ErrCannotDeletePanelPrimary)
	require.NoError(t, mock.ExpectationsWereMet()) // No DELETE expected.
}

// TestDomainRepository_Delete_AllowsRegularDomain: regression test that
// Delete still works for non-panel-primary rows after the guard was added.
func TestDomainRepository_Delete_AllowsRegularDomain(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT `id`,`is_panel_primary` FROM `domains` WHERE id = \\?.*LIMIT").
		WithArgs("dom_regular", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "is_panel_primary"},
		).AddRow("dom_regular", false))

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `domains` WHERE id = \\?").
		WithArgs("dom_regular").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectDomainPythonAppsDeleted(mock, "dom_regular")
	mock.ExpectCommit()

	err := repo.Delete(context.Background(), "dom_regular")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// expectDomainPythonAppsDeleted expects the deletes of a domain's Python apps
// and their env rows. python_apps has no foreign key to domains, so a domain's
// delete takes them itself; an app left behind kept running on its port with
// no vhost (GH #2289).
func expectDomainPythonAppsDeleted(mock sqlmock.Sqlmock, domainID string) {
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `python_app_env` WHERE app_id IN (SELECT `id` FROM `python_apps` WHERE domain_id = ?)")).
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `python_apps` WHERE domain_id = ?")).
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// A domain row that is gone by the time of the delete leaves the apps alone.
func TestDomainRepository_Delete_RowGoneKeepsTheApps(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT `id`,`is_panel_primary` FROM `domains` WHERE id = \\?.*LIMIT").
		WithArgs("dom_raced", 1).
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "is_panel_primary"},
		).AddRow("dom_raced", false))
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `domains` WHERE id = \\?").
		WithArgs("dom_raced").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.Delete(context.Background(), "dom_raced")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_Delete_NotFound: regression test for the pre-existing
// ErrNotFound path — the new guard's early-load must propagate NotFound.
func TestDomainRepository_Delete_NotFound(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectQuery("SELECT `id`,`is_panel_primary` FROM `domains` WHERE id = \\?.*LIMIT").
		WithArgs("dom_ghost", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "is_panel_primary"})) // empty

	err := repo.Delete(context.Background(), "dom_ghost")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
