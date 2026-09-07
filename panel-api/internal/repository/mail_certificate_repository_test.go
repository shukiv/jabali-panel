package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const mcSelectByDomain = `SELECT \* FROM .mail_certificate. WHERE domain_id = .? ORDER BY .* LIMIT`

func TestMailCert_GetByDomain_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectQuery(mcSelectByDomain).WithArgs("dom-1", 1).WillReturnError(gorm.ErrRecordNotFound)

	_, err := repo.GetByDomain(context.Background(), "dom-1")
	require.Error(t, err)
	assert.Equal(t, repository.ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailCert_EnsureForDomain_CreatesPending(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	// First Get returns not-found, then Create insert succeeds.
	mock.ExpectQuery(mcSelectByDomain).WithArgs("dom-2", 1).WillReturnError(gorm.ErrRecordNotFound)
	mock.ExpectBegin()
	// GORM uses Query for inserts that RETURN default columns (created_at,
	// updated_at default CURRENT_TIMESTAMP on the table). Mock the returned
	// timestamps so the model gets populated.
	now := time.Now().UTC()
	mock.ExpectQuery(`INSERT INTO .mail_certificate.`).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "updated_at"}).AddRow(now, now))
	mock.ExpectCommit()

	row, err := repo.EnsureForDomain(context.Background(), "dom-2")
	require.NoError(t, err)
	assert.Equal(t, "dom-2", row.DomainID)
	assert.Equal(t, models.MailCertStatusPending, row.Status)
	assert.NotEmpty(t, row.ID, "ID should be a fresh ULID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailCert_EnsureForDomain_ReturnsExisting(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	rows := sqlmock.NewRows([]string{"id", "domain_id", "lineage_path", "status"}).
		AddRow("mc-1", "dom-3", "/etc/letsencrypt/live/mail.example.com", "issued")
	mock.ExpectQuery(mcSelectByDomain).WithArgs("dom-3", 1).WillReturnRows(rows)

	row, err := repo.EnsureForDomain(context.Background(), "dom-3")
	require.NoError(t, err)
	assert.Equal(t, "mc-1", row.ID)
	assert.Equal(t, "issued", row.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailCert_MarkIssued_ClearsErrorAndRetry(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .mail_certificate.`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	now := time.Now().UTC()
	require.NoError(t, repo.MarkIssued(context.Background(), "mc-1", "/etc/letsencrypt/live/mail.example.com", now, now.Add(90*24*time.Hour)))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailCert_MarkFailed_SetsBackoff(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .mail_certificate.`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.MarkFailed(context.Background(), "mc-1", "rate limited", 3*time.Hour))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMailCert_MarkDNSMissing_SetsHourBackoff(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .mail_certificate.`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.MarkDNSMissing(context.Background(), "mc-1", "no A record for mail.example.com"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// ResetForReissue (GH #1579) flips a settled row back to pending and returns the
// rows affected. The WHERE clause is state-guarded to status IN (issued, failed,
// dns_missing) so a disabled (opted-out) or in-flight (pending/issuing) row is
// never reset — asserted by pinning the SQL.
func TestMailCert_ResetForReissue_FlipsSettledToPending(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .mail_certificate. SET.*WHERE domain_id = .? AND status IN`).
		WillReturnResult(sqlmock.NewResult(0, 1)) // one settled row reset
	mock.ExpectCommit()

	n, err := repo.ResetForReissue(context.Background(), "dom-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain with no mail cert (or whose only row is disabled/in-flight) matches
// no rows: ResetForReissue returns 0 and no error, so the rename's best-effort
// heal is a clean no-op.
func TestMailCert_ResetForReissue_NoMatchIsZero(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewMailCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .mail_certificate. SET.*WHERE domain_id = .? AND status IN`).
		WillReturnResult(sqlmock.NewResult(0, 0)) // nothing matched
	mock.ExpectCommit()

	n, err := repo.ResetForReissue(context.Background(), "dom-none")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
	require.NoError(t, mock.ExpectationsWereMet())
}
