package repository_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestSSLCertificateRepository_Create(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	now := time.Now()
	cert := &models.SSLCertificate{
		ID:        "01ARWX4FRYXZ73AK7EQQ69G5NV",
		DomainID:  "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Status:    models.SSLStatusPending,
		Staging:   false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `ssl_certificates`")).
		WithArgs(
			cert.ID,
			cert.DomainID,
			models.SSLStatusPending,
			nil,              // issued_at
			nil,              // expires_at
			0,                // renewal_count
			nil,              // last_renewed_at
			nil,              // last_error
			false,            // staging
			nil,              // cert_path
			nil,              // key_path
			nil,              // next_retry_at
			0,                // retry_count
			"",               // issue_method (JAB-235)
			nil,              // last_attempt_at
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.Create(ctx, cert)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_FindByDomainID_Success(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	domainID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	now := time.Now()

	// GORM's First() adds ORDER BY and LIMIT automatically
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `ssl_certificates` WHERE domain_id = ? ORDER BY `ssl_certificates`.`id` LIMIT ?")).
		WithArgs(domainID, 1).
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "domain_id", "status", "issued_at", "expires_at",
				"renewal_count", "last_renewed_at", "last_error", "staging",
				"cert_path", "key_path", "next_retry_at", "retry_count", "created_at", "updated_at",
			}).AddRow(
				"01ARWX4FRYXZ73AK7EQQ69G5NV",
				domainID,
				models.SSLStatusIssued,
				now.Add(-30*24*time.Hour),
				now.Add(60*24*time.Hour),
				0,
				nil,
				nil,
				false,
				"/etc/letsencrypt/live/example.com/fullchain.pem",
				"/etc/letsencrypt/live/example.com/privkey.pem",
				nil, // next_retry_at
				0,   // retry_count
				now,
				now,
			),
		)

	cert, err := repo.FindByDomainID(ctx, domainID)
	require.NoError(t, err)
	assert.NotNil(t, cert)
	assert.Equal(t, models.SSLStatusIssued, cert.Status)
	assert.Equal(t, domainID, cert.DomainID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_FindByDomainID_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	domainID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	// GORM's First() adds ORDER BY and LIMIT automatically
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `ssl_certificates` WHERE domain_id = ? ORDER BY `ssl_certificates`.`id` LIMIT ?")).
		WithArgs(domainID, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "domain_id", "status", "issued_at", "expires_at",
			"renewal_count", "last_renewed_at", "last_error", "staging",
			"cert_path", "key_path", "next_retry_at", "retry_count", "last_attempt_at", "created_at", "updated_at",
		}))

	cert, err := repo.FindByDomainID(ctx, domainID)
	require.Error(t, err)
	assert.Equal(t, repository.ErrNotFound, err)
	assert.Nil(t, cert)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_UpdateStatus(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	certID := "01ARWX4FRYXZ73AK7EQQ69G5NV"
	errorMsg := "rate limited"

	mock.ExpectBegin()
	// GORM generates UPDATE with SET columns in non-deterministic order (map iteration)
	// Use AnyArg for all value args to avoid flakiness
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `ssl_certificates` SET")).
		WithArgs(
			sqlmock.AnyArg(), // last_attempt_at (new field)
			sqlmock.AnyArg(), // status or last_error
			sqlmock.AnyArg(), // last_error or status
			sqlmock.AnyArg(), // updated_at
			certID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateStatus(ctx, certID, models.SSLStatusFailed, &errorMsg)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_UpdateAfterIssuance(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	certID := "01ARWX4FRYXZ73AK7EQQ69G5NV"
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(90 * 24 * time.Hour)
	certPath := "/etc/letsencrypt/live/example.com/fullchain.pem"
	keyPath := "/etc/letsencrypt/live/example.com/privkey.pem"

	mock.ExpectBegin()
	// GORM generates UPDATE with SET columns in non-deterministic order (map iteration)
	// Use AnyArg for the UPDATE values; only check the WHERE clause argument (certID)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `ssl_certificates` SET")).
		WithArgs(
			sqlmock.AnyArg(), // cert_path
			sqlmock.AnyArg(), // expires_at
			sqlmock.AnyArg(), // issued_at
			sqlmock.AnyArg(), // key_path,
			sqlmock.AnyArg(), // last_attempt_at
			sqlmock.AnyArg(), // status
			sqlmock.AnyArg(), // updated_at
			certID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.UpdateAfterIssuance(ctx, certID, issuedAt, expiresAt, certPath, keyPath)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_DeleteByDomainID(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	domainID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `ssl_certificates` WHERE domain_id = ?")).
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.DeleteByDomainID(ctx, domainID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_ListAll(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	now := time.Now()

	// Expect a SELECT joining ssl_certificates, domains, and users
	mock.ExpectQuery(
		regexp.QuoteMeta("SELECT sc.id, sc.domain_id, d.name as domain_name,\n\t\t        d.user_id, u.username as user_username,\n\t\t        sc.status, sc.issued_at, sc.expires_at,\n\t\t        sc.renewal_count, sc.last_renewed_at, sc.last_error, sc.staging, sc.last_attempt_at,\n\t\t        sc.cert_path FROM ssl_certificates sc JOIN domains d ON sc.domain_id = d.id JOIN users u ON d.user_id = u.id WHERE sc.status <> ? ORDER BY sc.created_at DESC")).
		WithArgs("revoked").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "domain_id", "domain_name", "user_id", "user_username",
				"status", "issued_at", "expires_at", "renewal_count", "last_renewed_at", "last_error", "staging", "last_attempt_at",
				// JAB-203: carried on the join so the observation pass can read the
				// REAL path rather than assuming /etc/letsencrypt/live/<domain>/,
				// which is wrong for an installed custom certificate.
				"cert_path",
			}).
				AddRow(
					"01ARWX4FRYXZ73AK7EQQ69G5NV",
					"01ARZ3NDEKTSV4RRFFQ69G5FAV",
					"example.com",
					"01ARZ3NDEKTSV4RRFFQ69G5F00",
					"testuser",
					models.SSLStatusIssued,
					now.Add(-30*24*time.Hour),
					now.Add(60*24*time.Hour),
					0,
					nil,
					nil,
					false,
					nil,
					nil, // cert_path
				).
				AddRow(
					"01ARWX4FRYXZ73AK7EQQ69G5N0",
					"01ARZ3NDEKTSV4RRFFQ69G5FA0",
					"example2.com",
					"01ARZ3NDEKTSV4RRFFQ69G5F01",
					"testuser2",
					models.SSLStatusPending,
					nil,
					nil,
					0,
					nil,
					nil,
					true,
					nil,
					nil, // cert_path
				),
		)

	certs, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, certs, 2)
	assert.Equal(t, "example.com", certs[0].DomainName)
	assert.Equal(t, "testuser", certs[0].UserUsername)
	assert.Equal(t, "example2.com", certs[1].DomainName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_ListByUserID(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	userID := "01ARZ3NDEKTSV4RRFFQ69G5F00"
	now := time.Now()

	// Expect a SELECT with WHERE on user_id
	mock.ExpectQuery(
		regexp.QuoteMeta("SELECT sc.id, sc.domain_id, d.name as domain_name,\n\t\t        d.user_id, u.username as user_username,\n\t\t        sc.status, sc.issued_at, sc.expires_at,\n\t\t        sc.renewal_count, sc.last_renewed_at, sc.last_error, sc.staging, sc.last_attempt_at FROM ssl_certificates sc JOIN domains d ON sc.domain_id = d.id JOIN users u ON d.user_id = u.id WHERE d.user_id = ? AND sc.status <> ? ORDER BY sc.created_at DESC")).
		WithArgs(userID, "revoked").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "domain_id", "domain_name", "user_id", "user_username",
				"status", "issued_at", "expires_at", "renewal_count", "last_renewed_at", "last_error", "staging", "last_attempt_at",
			}).
				AddRow(
					"01ARWX4FRYXZ73AK7EQQ69G5NV",
					"01ARZ3NDEKTSV4RRFFQ69G5FAV",
					"example.com",
					userID,
					"testuser",
					models.SSLStatusIssued,
					now.Add(-30*24*time.Hour),
					now.Add(60*24*time.Hour),
					1,
					now.Add(-7*24*time.Hour),
					nil,
					false,
					nil,
				),
		)

	certs, err := repo.ListByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, certs, 1)
	assert.Equal(t, userID, certs[0].UserID)
	assert.Equal(t, "example.com", certs[0].DomainName)
	assert.Equal(t, "testuser", certs[0].UserUsername)
	assert.Equal(t, 1, certs[0].RenewalCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSSLCertificateRepository_ListByUserID_Empty(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := repository.NewSSLCertificateRepository(gdb)
	ctx := context.Background()

	userID := "01ARZ3NDEKTSV4RRFFQ69G5F99"

	// Expect an empty result set
	mock.ExpectQuery(
		regexp.QuoteMeta("SELECT sc.id, sc.domain_id, d.name as domain_name,\n\t\t        d.user_id, u.username as user_username,\n\t\t        sc.status, sc.issued_at, sc.expires_at,\n\t\t        sc.renewal_count, sc.last_renewed_at, sc.last_error, sc.staging, sc.last_attempt_at FROM ssl_certificates sc JOIN domains d ON sc.domain_id = d.id JOIN users u ON d.user_id = u.id WHERE d.user_id = ? AND sc.status <> ? ORDER BY sc.created_at DESC")).
		WithArgs(userID, "revoked").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "domain_id", "domain_name", "user_id", "user_username",
			"status", "issued_at", "expires_at", "renewal_count", "last_renewed_at", "last_error", "staging", "last_attempt_at",
		}))

	certs, err := repo.ListByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, certs, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}
