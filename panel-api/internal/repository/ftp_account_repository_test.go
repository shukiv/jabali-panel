package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newMockFtpDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))

	gdb, err := gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      raw,
		SkipInitializeWithVersion: false,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	return gdb, mock, raw
}

func testFtpAccount(now time.Time) *models.FtpAccount {
	return &models.FtpAccount{
		ID:         "01HFTPACCT0000000000000000",
		UserID:     "01HUSER000000000000000000A",
		Username:   "shop_deploy",
		HomePath:   "/home/shop/example.com/public_html",
		FTPAccess:  false,
		SFTPAccess: true,
		IsEnabled:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestFtpAccountCreate_Success(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `ftp_accounts` (`id`,`user_id`,`username`,`home_path`,`ftp_access`,`sftp_access`,`is_enabled`,`uid`,`isolated`,`quota_mb`,`jail_path`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)")).
		WithArgs(acct.ID, acct.UserID, acct.Username, acct.HomePath,
			acct.FTPAccess, acct.SFTPAccess, acct.IsEnabled,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), acct))
	require.NoError(t, mock.ExpectationsWereMet())
}

// The INSERT must carry an explicit sftp_access value even when it is false.
// The column default is 1, and a missing/defaulted bool would silently
// resurrect access the caller disabled (the recurring gorm default:1 scar —
// the model tag intentionally carries no default).
func TestFtpAccountCreate_ExplicitFalseSFTPAccess(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())
	acct.SFTPAccess = false
	acct.FTPAccess = true

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `ftp_accounts` (`id`,`user_id`,`username`,`home_path`,`ftp_access`,`sftp_access`,`is_enabled`,`uid`,`isolated`,`quota_mb`,`jail_path`,`created_at`,`updated_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)")).
		WithArgs(acct.ID, acct.UserID, acct.Username, acct.HomePath,
			true, false, acct.IsEnabled,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), acct))
	require.NoError(t, mock.ExpectationsWereMet())
}

// JAB-262: ReserveWithinCap locks the tenant's users row FOR UPDATE, counts,
// and inserts — all in one transaction — so concurrent creates serialize and
// cannot exceed the cap. These pin the query STRUCTURE (the lock + count +
// insert order); the 20-way concurrency proof is a real-DB integration check.
func TestFtpAccountReserveWithinCap_UnderCapInserts(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(acct.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(acct.UserID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `ftp_accounts` WHERE user_id = ?")).
		WithArgs(acct.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec("INSERT INTO `ftp_accounts`").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.ReserveWithinCap(context.Background(), acct, 3, 0))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountReserveWithinCap_AtCapRejectsNoInsert(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()
	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(acct.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(acct.UserID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `ftp_accounts` WHERE user_id = ?")).
		WithArgs(acct.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectRollback()

	err := repo.ReserveWithinCap(context.Background(), acct, 3, 0)
	require.ErrorIs(t, err, ErrFtpCapExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountCreate_DuplicateUsername(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `ftp_accounts`").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'shop_deploy' for key 'ux_ftp_accounts_username'"})
	mock.ExpectRollback()

	err := repo.Create(context.Background(), acct)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountFindByID_NotFound(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)

	mock.ExpectQuery("SELECT \\* FROM `ftp_accounts`").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err := repo.FindByID(context.Background(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountFindByIDAndUserID_ScopesToOwner(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `ftp_accounts` WHERE id = ? AND user_id = ?")).
		WithArgs("acct1", "userA", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "username"}).
			AddRow("acct1", "userA", "shop_deploy"))

	acct, err := repo.FindByIDAndUserID(context.Background(), "acct1", "userA")
	require.NoError(t, err)
	require.Equal(t, "userA", acct.UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Update must write ONLY the mutable columns — id/user_id/username/created_at
// stay out of the SET list (Select allowlist; a full-struct save would
// zero-write untouched columns).
func TestFtpAccountUpdate_AllowlistOnly(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)
	acct := testFtpAccount(time.Now())
	acct.IsEnabled = false

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `ftp_accounts` SET `ftp_access`=?,`home_path`=?,`is_enabled`=?,`sftp_access`=?,`updated_at`=? WHERE id = ?")).
		WithArgs(acct.FTPAccess, acct.HomePath, false, acct.SFTPAccess, sqlmock.AnyArg(), acct.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Update(context.Background(), acct))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountCountByUserID(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM `ftp_accounts` WHERE user_id = ?")).
		WithArgs("userA").
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(3))

	n, err := repo.CountByUserID(context.Background(), "userA")
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFtpAccountDelete(t *testing.T) {
	db, mock, raw := newMockFtpDB(t)
	defer raw.Close()

	repo := NewFtpAccountRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `ftp_accounts` WHERE id = ?")).
		WithArgs("acct1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Delete(context.Background(), "acct1"))
	require.NoError(t, mock.ExpectationsWereMet())
}
