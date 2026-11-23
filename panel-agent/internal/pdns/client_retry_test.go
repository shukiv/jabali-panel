package pdns

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

var deadlock = &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"}

// expectUpsert queues one UpsertZoneWithMeta transaction for zone example.com
// (new zone, one SOA record, no metadata) whose record insert returns recErr.
func expectUpsert(mock sqlmock.Sqlmock, recErr error) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM domains WHERE name = \?`).WithArgs("example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`INSERT INTO domains`).WithArgs("example.com").WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectExec(`DELETE FROM records WHERE domain_id = \?`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	ins := mock.ExpectPrepare(`INSERT INTO records`).ExpectExec()
	if recErr != nil {
		ins.WillReturnError(recErr)
		mock.ExpectRollback()
		return
	}
	ins.WillReturnResult(sqlmock.NewResult(1, 1))
	for range 2 { // ALLOW-AXFR-FROM, ALSO-NOTIFY: cleared, nothing to insert
		mock.ExpectExec(`DELETE FROM domainmetadata`).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectCommit()
}

func newMockClient(t *testing.T) (*Client, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	old := lockRetryBackoff
	lockRetryBackoff = time.Millisecond
	t.Cleanup(func() { lockRetryBackoff = old })
	return &Client{db: db}, mock
}

var soa = []Record{{Name: "example.com", Type: "SOA", Content: "ns1.example.com. hostmaster.example.com. 1 10800 3600 604800 3600", TTL: 3600}}

// Two zones created at the same moment: MariaDB picks one transaction as the
// deadlock victim and rolls it back. Run again, it commits.
func TestUpsertZoneWithMeta_RetriesADeadlock(t *testing.T) {
	c, mock := newMockClient(t)
	expectUpsert(mock, deadlock)
	expectUpsert(mock, nil)

	id, err := c.UpsertZoneWithMeta("example.com", soa, UpsertZoneOptions{})
	if err != nil || id != 7 {
		t.Fatalf("UpsertZoneWithMeta = %d, %v; want 7, nil after one retry", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Any other error is returned at once, and a deadlock that keeps coming back
// gives up after lockRetryAttempts.
func TestUpsertZoneWithMeta_RetryLimits(t *testing.T) {
	c, mock := newMockClient(t)
	expectUpsert(mock, &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	if _, err := c.UpsertZoneWithMeta("example.com", soa, UpsertZoneOptions{}); err == nil {
		t.Fatal("a duplicate-key error must fail")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a non-lock error was retried: %v", err)
	}

	c, mock = newMockClient(t)
	for range lockRetryAttempts {
		expectUpsert(mock, deadlock)
	}
	_, err := c.UpsertZoneWithMeta("example.com", soa, UpsertZoneOptions{})
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1213 {
		t.Fatalf("err = %v, want the deadlock after %d attempts", err, lockRetryAttempts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIsLockConflict(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{deadlock, true},
		{&mysql.MySQLError{Number: 1205}, true},
		{&mysql.MySQLError{Number: 1062}, false},
		{sql.ErrNoRows, false},
		{nil, false},
	} {
		wrapped := tc.err
		if wrapped != nil {
			wrapped = errors.Join(errors.New("insert example.com SOA"), tc.err)
		}
		if got := isLockConflict(wrapped); got != tc.want {
			t.Errorf("isLockConflict(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
