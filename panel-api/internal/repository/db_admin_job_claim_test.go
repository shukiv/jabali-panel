package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A running maintenance job claims its engine through running_engine, which
// carries a unique key (migration 000346): two callers that both passed the
// RunningJob check cannot both insert. These pin the claim and its release.

func TestDBAdminCreateJob_RunningClaimsEngine(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDBAdminRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `db_admin_jobs` .*`running_engine`").
		WithArgs(sqlmock.AnyArg(), "mariadb", "maintenance", "all", "running",
			sqlmock.AnyArg(), "u1", sqlmock.AnyArg(), sqlmock.AnyArg(), "mariadb").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	j := &models.DBAdminJob{Engine: "mariadb", Kind: "maintenance", Scope: "all", Status: "running", ActorUserID: "u1"}
	require.NoError(t, repo.CreateJob(context.Background(), j))
	require.NotNil(t, j.RunningEngine)
	require.Equal(t, "mariadb", *j.RunningEngine)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBAdminCreateJob_SecondRunningJobIsConflict(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDBAdminRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `db_admin_jobs`").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'mariadb' for key 'uniq_db_admin_jobs_running_engine'"})
	mock.ExpectRollback()

	err := repo.CreateJob(context.Background(), &models.DBAdminJob{Engine: "mariadb", Kind: "maintenance", Scope: "all", ActorUserID: "u1"})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBAdminCreateJob_FinishedJobClaimsNothing(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDBAdminRepository(db)

	engine := "mariadb"
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `db_admin_jobs`").
		WithArgs(sqlmock.AnyArg(), "mariadb", "maintenance", "all", "ok",
			sqlmock.AnyArg(), "u1", sqlmock.AnyArg(), sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	j := &models.DBAdminJob{Engine: "mariadb", Kind: "maintenance", Scope: "all", Status: "ok", ActorUserID: "u1", RunningEngine: &engine}
	require.NoError(t, repo.CreateJob(context.Background(), j))
	require.Nil(t, j.RunningEngine)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBAdminFinishJob_ReleasesEngine(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewDBAdminRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `db_admin_jobs` SET `finished_at`=?,`running_engine`=?,`status`=?,`summary`=? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), nil, "ok", "done", "j1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.FinishJob(context.Background(), "j1", "ok", "done"))
	require.NoError(t, mock.ExpectationsWereMet())
}
