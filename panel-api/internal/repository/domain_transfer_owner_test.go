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
	mock.ExpectQuery("SELECT `nginx_rules`,`docker_app_id` FROM `domains` WHERE id = .+ FOR UPDATE").
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
	mock.ExpectQuery("SELECT `nginx_rules`,`docker_app_id` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"nginx_rules"}))
	mock.ExpectRollback()

	err := repo.TransferOwner(context.Background(), domainID, "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html")
	require.True(t, errors.Is(err, ErrNotFound), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// expectTransferOwnerTail expects the deletes that follow the domain update
// of an owner change, and the commit.
func expectTransferOwnerTail(mock sqlmock.Sqlmock, domainID string) {
	expectDomainPythonAppsDeleted(mock, domainID)
	mock.ExpectExec("DELETE FROM `domain_git_repos` WHERE domain_id = ").
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM `domain_composer_settings` WHERE domain_id = ").
		WithArgs(domainID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

// GH #2289: a Docker app of another account, linked onto a domain that
// changes owner, is unlinked: the link goes, and so do the proxy_pass rules
// to its ports. The domain's other rules stay.
func TestDomain_TransferOwnerUnlinksAnotherAccountsDockerApp(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	const domainID, appID = "01DOMAIN0000000000000000AB", "01DOCKER0000000000000000AB"
	ws := true
	kept := models.NginxRules{
		{Type: "proxy_pass", Path: "/api", Target: "http://127.0.0.1:30002", Websocket: &ws},
		{Type: "deny_paths", Path: "/", Extensions: []string{"sql"}},
	}
	all := append(models.NginxRules{
		{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:30001", Websocket: &ws},
	}, kept...)
	allJSON, err := json.Marshal(all)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `nginx_rules`,`docker_app_id` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"nginx_rules", "docker_app_id"}).AddRow(allJSON, appID))
	mock.ExpectQuery("SELECT \\* FROM `python_apps` WHERE domain_id = .+ FOR UPDATE").
		WithArgs(domainID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT `id`,`user_id` FROM `docker_apps` WHERE id = ").
		WithArgs(appID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(appID, "01OLDUSR0000000000000000AB"))
	mock.ExpectQuery("SELECT \\* FROM `docker_app_published_ports` WHERE app_id = ").
		WithArgs(appID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "app_id", "host_port"}).AddRow("01PORTAA0000000000000000AB", appID, 30001))
	mock.ExpectExec("UPDATE `domains` SET .+ WHERE id = ").
		WithArgs("/home/bob/domains/example.com/public_html", nil, rulesArg{want: kept}, "01NEWUSR0000000000000000AB", sqlmock.AnyArg(), domainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectTransferOwnerTail(mock, domainID)

	require.NoError(t, repo.TransferOwner(context.Background(), domainID, "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A server-level Docker app (no owner) and the new owner's own app stay
// linked, with their rules.
func TestDomain_TransferOwnerKeepsServerAndNewOwnerDockerApps(t *testing.T) {
	const domainID, appID, newUser = "01DOMAIN0000000000000000AB", "01DOCKER0000000000000000AB", "01NEWUSR0000000000000000AB"
	for name, owner := range map[string]any{"server-level": nil, "new owner's": newUser} {
		t.Run(name, func(t *testing.T) {
			db, mock, raw := newMockDB(t)
			defer raw.Close()
			repo := NewDomainRepository(db)

			mock.ExpectBegin()
			mock.ExpectQuery("SELECT `nginx_rules`,`docker_app_id` FROM `domains` WHERE id = .+ FOR UPDATE").
				WillReturnRows(sqlmock.NewRows([]string{"nginx_rules", "docker_app_id"}).
					AddRow(`[{"type":"proxy_pass","path":"/","target":"http://127.0.0.1:30001"}]`, appID))
			mock.ExpectQuery("SELECT \\* FROM `python_apps` WHERE domain_id = .+ FOR UPDATE").
				WithArgs(domainID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectQuery("SELECT `id`,`user_id` FROM `docker_apps` WHERE id = ").
				WithArgs(appID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(appID, owner))
			// Only the owner and the docroot change.
			mock.ExpectExec("UPDATE `domains` SET .+ WHERE id = ").
				WithArgs("/home/bob/domains/example.com/public_html", newUser, sqlmock.AnyArg(), domainID).
				WillReturnResult(sqlmock.NewResult(0, 1))
			expectTransferOwnerTail(mock, domainID)

			require.NoError(t, repo.TransferOwner(context.Background(), domainID, newUser, "/home/bob/domains/example.com/public_html"))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// A link to a Docker app that is gone goes too.
func TestDomain_TransferOwnerDropsALinkToAGoneDockerApp(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()
	repo := NewDomainRepository(db)

	const domainID, appID = "01DOMAIN0000000000000000AB", "01DOCKER0000000000000000AB"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `nginx_rules`,`docker_app_id` FROM `domains` WHERE id = .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"nginx_rules", "docker_app_id"}).AddRow(nil, appID))
	mock.ExpectQuery("SELECT \\* FROM `python_apps` WHERE domain_id = .+ FOR UPDATE").
		WithArgs(domainID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT `id`,`user_id` FROM `docker_apps` WHERE id = ").
		WithArgs(appID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}))
	mock.ExpectQuery("SELECT \\* FROM `docker_app_published_ports` WHERE app_id = ").
		WithArgs(appID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("UPDATE `domains` SET .+ WHERE id = ").
		WithArgs("/home/bob/domains/example.com/public_html", nil, sqlmock.AnyArg(), "01NEWUSR0000000000000000AB", sqlmock.AnyArg(), domainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectTransferOwnerTail(mock, domainID)

	require.NoError(t, repo.TransferOwner(context.Background(), domainID, "01NEWUSR0000000000000000AB", "/home/bob/domains/example.com/public_html"))
	require.NoError(t, mock.ExpectationsWereMet())
}
