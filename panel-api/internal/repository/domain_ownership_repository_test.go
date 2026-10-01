package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1816: every ownership state change is conditional, so a check result
// can never undo an admin revoke or approve that happened meanwhile.

func TestDomainOwnership_MarkVerified_IsConditionalOnPendingAndToken(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET .*`ownership_status`=\\?.* WHERE \\(id = \\? AND ownership_status = \\?\\) AND ownership_token = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "d1", models.OwnershipPending, "tok").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	changed, err := repo.MarkDomainVerified(context.Background(), "d1", models.OwnershipMethodDNSTXT, "tok", at)
	require.NoError(t, err)
	require.False(t, changed, "a row that is no longer pending with this token must not change")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_MarkVerified_AdminApprovalSkipsTheToken(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `web_domain_aliases` SET .* WHERE id = \\? AND ownership_status = \\?$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	changed, err := repo.MarkAliasVerified(context.Background(), "a1", models.OwnershipMethodAdmin, "", time.Now())
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_MarkPending_FromVerifiedOnly(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET .*`ownership_token`=\\?.* WHERE id = \\? AND ownership_status = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	changed, err := repo.MarkDomainPending(context.Background(), "d1", "newtok", time.Now(), true, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())

	_, err = repo.MarkDomainPending(context.Background(), "d1", "", time.Now(), false, false)
	require.Error(t, err, "a pending row without a token could never be proven")
}

func TestDomainOwnership_RecordCheck_OnlyForThePendingToken(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET .* WHERE id = \\? AND ownership_status = \\? AND ownership_token = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	now := time.Now()
	changed, err := repo.RecordDomainCheck(context.Background(), "d1", "tok", models.OwnershipResultNotFound, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_EnsureToken_OnlyWhenEmpty(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET `ownership_token`=\\? WHERE id = \\? AND ownership_token = \\?").
		WithArgs("tok", "d1", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	changed, err := repo.EnsureDomainToken(context.Background(), "d1", "tok")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_ListDue(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `domains` WHERE ownership_status = ? AND (ownership_next_check_at IS NULL OR ownership_next_check_at <= ?) ORDER BY ownership_next_check_at IS NOT NULL, ownership_next_check_at ASC LIMIT ?")).
		WithArgs(models.OwnershipPending, now, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ownership_status"}).AddRow("d1", "a.test", "pending"))

	rows, err := repo.ListDomainsDue(context.Background(), now, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a.test", rows[0].Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_ListParentProvenUnder_ChecksTheLabelBoundary(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `domains` WHERE ownership_status = ? AND ownership_method = ? AND name LIKE ?")).
		WithArgs(models.OwnershipVerified, models.OwnershipMethodParent, "%.example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow("d1", "shop.example.com").
			AddRow("d2", "a.b.example.com"))

	rows, err := repo.ListParentProvenUnder(context.Background(), "Example.com")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_GetSettings_MissingRowRequiresProof(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `domain_ownership_settings` WHERE id = ? LIMIT ?")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "require_proof"}))

	s, err := repo.GetSettings(context.Background())
	require.NoError(t, err)
	require.True(t, s.RequireProof, "a missing policy row must mean proof is required")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_SetRequireProof_Upserts(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)
	at := time.Now()

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO domain_ownership_settings (id, require_proof, updated_by, updated_at) VALUES (1, ?, ?, ?) ON DUPLICATE KEY UPDATE")).
		WithArgs(false, "admin@example.test", at).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, repo.SetRequireProof(context.Background(), false, "admin@example.test", at))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_ListParentProvenAliasesUnder_ChecksTheLabelBoundary(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `web_domain_aliases` WHERE ownership_status = ? AND ownership_method = ? AND hostname LIKE ?")).
		WithArgs(models.OwnershipVerified, models.OwnershipMethodParent, "%.example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "hostname"}).
			AddRow("a1", "www.example.com").
			AddRow("a2", "www.notexample.com"))

	rows, err := repo.ListParentProvenAliasesUnder(context.Background(), "Example.com")
	require.NoError(t, err)
	require.Len(t, rows, 1, "only a hostname under the label boundary is a descendant")
	require.Equal(t, "a1", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// GH #1816: the expiry sweep deletes a domain only while it is still an
// expired, never-verified claim. A verification that raced the sweep keeps
// the row, and the delete reports ErrOwnershipChanged.
func TestDomainOwnership_DeleteExpiredDomain_IsConditional(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)
	cutoff := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	del := regexp.QuoteMeta("DELETE FROM `domains` WHERE id = ? AND ownership_status = ? AND ownership_verified_at IS NULL AND ownership_pending_since <= ? AND is_panel_primary = ? AND managed_by <> ?")
	mock.ExpectBegin()
	mock.ExpectExec(del).
		WithArgs("d1", models.OwnershipPending, cutoff, false, models.DomainManagedByDockerApp).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.ErrorIs(t, repo.DeleteExpiredDomain(context.Background(), "d1", cutoff), ErrOwnershipChanged)

	mock.ExpectBegin()
	mock.ExpectExec(del).
		WithArgs("d2", models.OwnershipPending, cutoff, false, models.DomainManagedByDockerApp).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.DeleteExpiredDomain(context.Background(), "d2", cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDomainOwnership_DeleteExpiredAlias_IsConditional(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainOwnershipRepository(db)
	cutoff := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `web_domain_aliases` WHERE id = ? AND ownership_status = ? AND ownership_verified_at IS NULL AND ownership_pending_since <= ?")).
		WithArgs("a1", models.OwnershipPending, cutoff).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.ErrorIs(t, repo.DeleteExpiredAlias(context.Background(), "a1", cutoff), ErrOwnershipChanged)
	require.NoError(t, mock.ExpectationsWereMet())
}
