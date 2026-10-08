package repository_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993 / JAB-374: the backup builder reads every domain's aliases in one
// query, not one per domain.
func TestWebDomainAlias_ListByDomainIDs(t *testing.T) {
	gdb, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := repository.NewWebDomainAliasRepository(gdb)

	mock.ExpectQuery("SELECT \\* FROM `web_domain_aliases` WHERE domain_id IN \\(\\?,\\?\\)").
		WithArgs("d1", "d2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "domain_id", "hostname"}).
			AddRow("a1", "d1", "shop.example.net").AddRow("a2", "d2", "blog.example.net"))

	got, err := repo.ListByDomainIDs(context.Background(), []string{"d1", "d2"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "shop.example.net", got[0].Hostname)
	require.NoError(t, mock.ExpectationsWereMet())

	none, err := repo.ListByDomainIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, none)
	require.NoError(t, mock.ExpectationsWereMet(), "no ids must not query")
}
