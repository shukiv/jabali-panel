package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1812: the owner's subdomain-delegation opt-in is written through the
// general domain Update, which is a Select-allowlist Updates. A new column that
// is NOT in that allowlist is silently dropped by GORM (the
// feedback_domain_update_allowlist_silent_drop class), so this test pins that
// `allow_subdomain_delegation` actually appears in the emitted UPDATE. Falsify
// by removing "allow_subdomain_delegation" from the Update Select list in
// domain_repository.go: the column vanishes from the SQL and this expectation
// goes unmet → RED.
func TestDomainRepository_Update_PersistsAllowSubdomainDelegation(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	repo := NewDomainRepository(db)

	// The Select-allowlist Updates forces every listed column into the SET
	// clause regardless of the value, so requiring the column name in the SQL
	// is sufficient and value-independent.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `domains` SET.*`allow_subdomain_delegation`").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.Update(context.Background(), &models.Domain{
		ID:                       "dom_1",
		Name:                     "example.com",
		AllowSubdomainDelegation: true,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
