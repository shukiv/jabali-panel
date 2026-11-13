package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Put writes every field, the set_by_admin mark included, over an existing
// row: a save by the owner after an administrator's makes it the owner's.
func TestWebmailBranding_PutWritesEveryField(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainWebmailBrandingRepository(gdb)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `domain_webmail_branding` .+ ON DUPLICATE KEY UPDATE " +
		"`app_name`=VALUES\\(`app_name`\\),`company_name`=VALUES\\(`company_name`\\)," +
		"`website_url`=VALUES\\(`website_url`\\),`privacy_url`=VALUES\\(`privacy_url`\\),`imprint_url`=VALUES\\(`imprint_url`\\)," +
		"`logo_light`=VALUES\\(`logo_light`\\),`logo_dark`=VALUES\\(`logo_dark`\\),`favicon`=VALUES\\(`favicon`\\)," +
		"`set_by_admin`=VALUES\\(`set_by_admin`\\),`updated_at`=VALUES\\(`updated_at`\\)$").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Put(context.Background(), &models.DomainWebmailBranding{
		DomainID: "dom", AppName: "A Mail", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// ListAll names each row's domain and says whether it applies: an
// administrator's, or the owner's package has webmail branding. A user with
// no package (LEFT JOIN, NULL) has none.
func TestWebmailBranding_ListAllSaysWhatApplies(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainWebmailBrandingRepository(gdb)

	mock.ExpectQuery("SELECT b\\.\\*, d\\.name AS domain_name, " +
		"\\(b\\.set_by_admin = 1 OR COALESCE\\(p\\.webmail_branding_enabled, 0\\) = 1\\) AS applies " +
		"FROM domain_webmail_branding AS b " +
		"JOIN domains d ON d\\.id = b\\.domain_id " +
		"JOIN users u ON u\\.id = d\\.user_id " +
		"LEFT JOIN hosting_packages p ON p\\.id = u\\.package_id ORDER BY d\\.name").
		WillReturnRows(sqlmock.NewRows([]string{"domain_id", "app_name", "logo_light", "set_by_admin", "domain_name", "applies"}).
			AddRow("dom1", "A Mail", "aaaaaaaaaaaa", false, "a.com", true).
			AddRow("dom2", "B Mail", "", false, "b.com", false))

	rows, err := repo.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "dom1", rows[0].DomainID)
	require.Equal(t, "A Mail", rows[0].AppName)
	require.Equal(t, "aaaaaaaaaaaa", rows[0].LogoLight)
	require.Equal(t, "a.com", rows[0].DomainName)
	require.True(t, rows[0].Applies)
	require.False(t, rows[1].Applies)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWebmailBranding_GetNotFound(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainWebmailBrandingRepository(gdb)

	mock.ExpectQuery("SELECT \\* FROM `domain_webmail_branding` WHERE domain_id = \\?").
		WithArgs("dom", 1).
		WillReturnRows(sqlmock.NewRows([]string{"domain_id"}))

	_, err := repo.Get(context.Background(), "dom")
	require.ErrorIs(t, err, repository.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ListByDomainIDs reads the branding of the backup's domains in one query,
// and none for no domains.
func TestWebmailBranding_ListByDomainIDs(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewDomainWebmailBrandingRepository(gdb)

	rows, err := repo.ListByDomainIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, rows)

	mock.ExpectQuery("SELECT \\* FROM `domain_webmail_branding` WHERE domain_id IN \\(\\?,\\?\\) ORDER BY domain_id").
		WithArgs("dom1", "dom2").
		WillReturnRows(sqlmock.NewRows([]string{"domain_id", "app_name", "favicon"}).
			AddRow("dom1", "A Mail", "bbbbbbbbbbbb"))

	rows, err = repo.ListByDomainIDs(context.Background(), []string{"dom1", "dom2"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "A Mail", rows[0].AppName)
	require.Equal(t, "bbbbbbbbbbbb", rows[0].Favicon)
	require.NoError(t, mock.ExpectationsWereMet())
}
