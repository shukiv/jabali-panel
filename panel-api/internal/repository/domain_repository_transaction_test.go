package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestDomainRepository_Transaction_CommitsAllOnSuccess: when every write the
// closure issues succeeds, the whole apply commits as one unit — BEGIN, both
// dedicated writes, COMMIT. This is the happy path the JAB-318 domain-apply
// (general Update + ssl_mode + cache_enabled) rides on.
func TestDomainRepository_Transaction_CommitsAllOnSuccess(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains`"). // UpdateSSLMode
						WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `domains`"). // UpdateCacheEnabled
						WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Transaction(context.Background(), func(tx DomainRepository) error {
		if err := tx.UpdateSSLMode(context.Background(), "dom_1", "le"); err != nil {
			return err
		}
		return tx.UpdateCacheEnabled(context.Background(), "dom_1", true)
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDomainRepository_Transaction_RollsBackOnError: the core AC4 guarantee. A
// dedicated writer failing mid-sequence must roll the ENTIRE apply back — the
// earlier write is undone (ROLLBACK, never COMMIT), so the row is never left
// half-patched. Falsify by unwrapping the impl (call fn(r) without
// db.Transaction): no BEGIN/ROLLBACK is emitted and the expectation set is
// unmet — the test reddens.
func TestDomainRepository_Transaction_RollsBackOnError(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	boom := errors.New("boom: dedicated writer failed")

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains`"). // UpdateSSLMode — succeeds
						WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `domains`"). // UpdateCacheEnabled — fails
						WillReturnError(boom)
	mock.ExpectRollback()

	err := repo.Transaction(context.Background(), func(tx DomainRepository) error {
		if err := tx.UpdateSSLMode(context.Background(), "dom_1", "le"); err != nil {
			return err
		}
		return tx.UpdateCacheEnabled(context.Background(), "dom_1", true)
	})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet()) // BEGIN + 2 writes + ROLLBACK, no COMMIT
}
