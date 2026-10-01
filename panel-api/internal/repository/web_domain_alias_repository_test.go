package repository_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-390: a panel mail hostname is refused while a tenant web alias sits
// under it. The lookup matches names a full label or more deeper, never the
// name itself, and LIKE metacharacters in the name match only literally.
func TestWebDomainAlias_FindStrictSubdomainHostnames(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewWebDomainAliasRepository(gdb)

	mock.ExpectQuery("SELECT `hostname` FROM `web_domain_aliases` WHERE hostname LIKE \\? ESCAPE").
		WithArgs(`%.mail.ex\_ample.net`).
		WillReturnRows(sqlmock.NewRows([]string{"hostname"}).AddRow("x.mail.ex_ample.net"))

	got, err := repo.FindStrictSubdomainHostnames(context.Background(), " Mail.Ex_ample.NET ")
	require.NoError(t, err)
	require.Equal(t, []string{"x.mail.ex_ample.net"}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWebDomainAlias_FindStrictSubdomainHostnames_EmptyName(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewWebDomainAliasRepository(gdb)

	got, err := repo.FindStrictSubdomainHostnames(context.Background(), "  ")
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet(), "an empty name must not query")
}

// GH #1816 / ADR-0170: the reconciler's alias list (vhost server_name and
// certificate SANs) holds VERIFIED aliases only; a pending alias never
// joins the served names.
func TestWebDomainAlias_ListHostnamesByDomainID_VerifiedOnly(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewWebDomainAliasRepository(gdb)

	mock.ExpectQuery("SELECT `hostname` FROM `web_domain_aliases` WHERE domain_id = \\? AND ownership_status = \\? ORDER BY hostname ASC").
		WithArgs("d1", "verified").
		WillReturnRows(sqlmock.NewRows([]string{"hostname"}).AddRow("shop.example.net"))

	got, err := repo.ListHostnamesByDomainID(context.Background(), "d1")
	require.NoError(t, err)
	require.Equal(t, []string{"shop.example.net"}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
