package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// rulesArg matches the nginx_rules value the update writes.
type rulesArg struct{ want models.NginxRules }

func (a rulesArg) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok {
		return false
	}
	var got models.NginxRules
	if err := json.Unmarshal(b, &got); err != nil {
		return false
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(a.want)
	return string(gotJSON) == string(wantJSON)
}

// GH #2289: a domain that changes owner loses its Python apps, which run as
// the old owner, and the nginx rules that send it to them, in the same
// transaction as the owner change. Rules that aren't the apps' stay.
func TestDomain_TransferOwnerDeletesThePythonApps(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	const domainID = "01DOMAIN0000000000000000AB"
	ws := true
	kept := models.NginxRules{
		{Type: "proxy_pass", Path: "/other", Target: "http://127.0.0.1:20001", Websocket: &ws},
		{Type: "static_alias", Path: "/assets/", Target: "/home/alice/apps/shopping/assets/"},
		{Type: "deny_paths", Path: "/", Extensions: []string{"sql"}},
	}
	all := append(models.NginxRules{
		{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:20000", Websocket: &ws},
		{Type: "static_alias", Path: "/static/", Target: "/home/alice/apps/shop/static/"},
		{Type: "media_alias", Path: "/media/", Target: "/home/alice/apps/shop/media/"},
	}, kept...)
	allJSON, err := json.Marshal(all)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `nginx_rules` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"nginx_rules"}).AddRow(allJSON))
	mock.ExpectQuery("SELECT \\* FROM `python_apps` WHERE domain_id = .+ FOR UPDATE").
		WithArgs(domainID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "domain_id", "app_root", "loopback_port"}).
			AddRow("01APPAAA0000000000000000AB", domainID, "/home/alice/apps/shop", 20000))
	mock.ExpectExec("UPDATE `domains` SET .+ WHERE id = ").
		WithArgs("/home/bob/domains/example.com/public_html", rulesArg{want: kept}, "01NEWUSR0000000000000000AB", sqlmock.AnyArg(), domainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectDomainPythonAppsDeleted(mock, domainID)
	mock.ExpectExec("DELETE FROM `domain_git_repos` WHERE domain_id = ").
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM `domain_composer_settings` WHERE domain_id = ").
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.TransferOwner(context.Background(), domainID, "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A domain row that is gone by the time of the owner change leaves the apps
// and the rules alone.
func TestDomain_TransferOwnerRowGoneKeepsTheApps(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	const domainID = "01DOMAIN0000000000000000AB"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `nginx_rules` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"nginx_rules"}))
	mock.ExpectRollback()

	err := repo.TransferOwner(context.Background(), domainID, "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html")
	require.True(t, errors.Is(err, ErrNotFound), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}
