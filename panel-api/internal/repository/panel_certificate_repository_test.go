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

const pcSelect = `SELECT \* FROM .panel_certificate. WHERE kind = .? ORDER BY .* LIMIT`

func TestPanelCert_GetByKind_NotFound(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	mock.ExpectQuery(pcSelect).WithArgs("mail", 1).WillReturnError(gorm.ErrRecordNotFound)

	_, err := repo.GetByKind(context.Background(), models.PanelCertKindMail)
	require.Error(t, err)
	assert.Equal(t, repository.ErrNotFound, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPanelCert_Get_IsHostnameKind(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	rows := sqlmock.NewRows([]string{"kind", "id", "hostname", "status", "cert_pem_path"}).
		AddRow("hostname", 1, "panel.example.com", "issued", "/etc/jabali/tls/panel.crt")
	mock.ExpectQuery(pcSelect).WithArgs("hostname", 1).WillReturnRows(rows)

	got, err := repo.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "hostname", got.Kind)
	assert.Equal(t, "issued", got.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPanelCert_EnsureDefault_CreatesBothKinds(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	// hostname kind: not found -> create
	mock.ExpectQuery(pcSelect).WithArgs("hostname", 1).WillReturnError(gorm.ErrRecordNotFound)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO .panel_certificate.`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	// mail kind: not found -> create
	mock.ExpectQuery(pcSelect).WithArgs("mail", 1).WillReturnError(gorm.ErrRecordNotFound)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO .panel_certificate.`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	host, err := repo.EnsureDefault(context.Background(), "panel.example.com")
	require.NoError(t, err)
	assert.Equal(t, "hostname", host.Kind)
	assert.Equal(t, "panel.example.com", host.Hostname)
	assert.Equal(t, "/etc/jabali/tls/panel.crt", host.CertPEMPath)
	assert.Equal(t, models.PanelCertStatusSelfSigned, host.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPanelCert_EnsureDefault_DoesNotRenameMailRow is the JAB-389 guard:
// when the panel hostname changes and both rows already exist, the hostname
// row is re-synced to the new FQDN (so the panel cert follows) but the mail
// row is left exactly as seeded — no silent cascade to mail.<new-fqdn>.
//
// The assertion is structural: sqlmock runs in strict-ordered mode, so the
// only writes it tolerates are the hostname row's re-sync. If ensureOne ever
// renamed the mail row, its SELECT+UPDATE would have no matching expectation
// and the test fails. Reverting the renameOnDrift guard reds this test.
func TestPanelCert_EnsureDefault_DoesNotRenameMailRow(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	hostRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"kind", "id", "hostname", "status", "cert_pem_path"}).
			AddRow("hostname", 1, "old.example.com", "issued", "/etc/jabali/tls/panel.crt")
	}

	// hostname kind exists at the OLD name -> drift -> re-synced to the new FQDN.
	mock.ExpectQuery(pcSelect).WithArgs("hostname", 1).WillReturnRows(hostRow())
	// Upsert re-reads the row, then updates it.
	mock.ExpectQuery(pcSelect).WithArgs("hostname", 1).WillReturnRows(hostRow())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .panel_certificate. SET .* WHERE kind = ?`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// mail kind exists at the ORIGINAL mail name. No UPDATE is expected: the
	// derived mail.new.example.com differs, but the mail row is never renamed.
	mock.ExpectQuery(pcSelect).WithArgs("mail", 1).WillReturnRows(
		sqlmock.NewRows([]string{"kind", "id", "hostname", "status", "cert_pem_path"}).
			AddRow("mail", 1, "mail.old.example.com", "issued", "/etc/jabali/tls/panel-mail.crt"))

	host, err := repo.EnsureDefault(context.Background(), "new.example.com")
	require.NoError(t, err)
	assert.Equal(t, "new.example.com", host.Hostname, "hostname row must follow the FQDN change")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPanelCert_ListAll(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	rows := sqlmock.NewRows([]string{"kind", "id", "hostname", "status"}).
		AddRow("hostname", 1, "panel.example.com", "issued").
		AddRow("mail", 1, "mail.panel.example.com", "pending_acme")
	mock.ExpectQuery(`SELECT \* FROM .panel_certificate. ORDER BY kind`).WillReturnRows(rows)

	got, err := repo.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "hostname", got[0].Kind)
	assert.Equal(t, "mail", got[1].Kind)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPanelCert_MarkIssuedKind_Mail(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .panel_certificate. SET .* WHERE kind = ?`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	iss := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	err := repo.MarkIssuedKind(context.Background(), models.PanelCertKindMail, iss, iss.Add(90*24*time.Hour))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPanelCert_MarkPendingRetry_Hostname(t *testing.T) {
	t.Parallel()
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewPanelCertificateRepository(gdb)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE .panel_certificate. SET .* WHERE kind = ?`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.MarkPendingRetry(context.Background(), "mail DNS not pointed at server", 3*time.Hour)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
