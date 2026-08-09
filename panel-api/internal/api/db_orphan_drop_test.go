package api

// A panel row for a MariaDB object must outlive a failed host-side drop.
//
// The delete/rollback paths used to log the agent failure and delete the row
// anyway. Because the agent call is a root RPC, an agent restart, a timeout,
// or a long-call 502 is enough to fail it — and once the row is gone the
// MariaDB database and login have nothing left to name them: they do not
// appear in `jabali db list`, no account backup includes them, and their
// grants stay live. Four such orphan pairs were found on testserver, two of
// them complete WordPress schemas (75 and 64 tables) that no backup covered.
//
// databases.go's own delete already gets this right (drop failure → 502, row
// kept); these tests hold the other paths to the same rule.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// dropFailAgent fails the named commands and records every call.
type dropFailAgent struct {
	mu       sync.Mutex
	failCmds map[string]bool
	calls    []string
}

func (a *dropFailAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.mu.Lock()
	a.calls = append(a.calls, cmd)
	fail := a.failCmds[cmd]
	a.mu.Unlock()
	if fail {
		return nil, errors.New("agent unavailable")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (a *dropFailAgent) called(cmd string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c == cmd {
			return true
		}
	}
	return false
}

// Repo fakes: embed the interface so only the methods these paths touch need
// implementing — anything else would panic loudly rather than pass silently.

type fakeInstallRepo struct {
	repository.ApplicationInstallRepository
	mu      sync.Mutex
	deleted []string
}

func (r *fakeInstallRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *fakeInstallRepo) UpdateStatus(_ context.Context, _, _ string, _, _ *string) error {
	return nil
}

type fakeDBRepo struct {
	repository.DatabaseRepository
	row     *models.Database
	mu      sync.Mutex
	deleted []string
}

func (r *fakeDBRepo) FindByID(_ context.Context, _ string) (*models.Database, error) {
	if r.row == nil {
		return nil, repository.ErrNotFound
	}
	return r.row, nil
}

func (r *fakeDBRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *fakeDBRepo) deleteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deleted)
}

type fakeDBUserRepo struct {
	repository.DatabaseUserRepository
	mu      sync.Mutex
	deleted []string
}

func (r *fakeDBUserRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *fakeDBUserRepo) deleteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deleted)
}

type fakeGrantRepo struct {
	repository.DatabaseUserGrantRepository
	mu      sync.Mutex
	deleted []string
}

func (r *fakeGrantRepo) ListByDatabaseUserID(_ context.Context, _ string) ([]models.DatabaseUserGrant, error) {
	return []models.DatabaseUserGrant{{ID: "grant-1"}}, nil
}

func (r *fakeGrantRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *fakeGrantRepo) deleteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deleted)
}

func newDeleteFixture(ag *dropFailAgent) (ApplicationHandlerConfig, *fakeInstallRepo, *fakeDBRepo, *fakeDBUserRepo, *fakeGrantRepo) {
	installs := &fakeInstallRepo{}
	dbs := &fakeDBRepo{row: &models.Database{ID: "db-1", Name: "alice_wp_abc123"}}
	dbUsers := &fakeDBUserRepo{}
	grants := &fakeGrantRepo{}
	return ApplicationHandlerConfig{
		ApplicationInstalls: installs,
		Databases:           dbs,
		DatabaseUsers:       dbUsers,
		DatabaseGrants:      grants,
		Agent:               ag,
	}, installs, dbs, dbUsers, grants
}

func runDelete(cfg ApplicationHandlerConfig) {
	createDeleteAndKickAgent(
		context.Background(),
		"install-1", "user-1", "wordpress", "",
		"db-1", "dbuser-1",
		"alice", "/home/alice/public_html", "example.com", "alice_wp_abc123",
		cfg,
	)
}

func TestWordPressDelete_KeepsPanelRowsWhenDatabaseDropFails(t *testing.T) {
	ag := &dropFailAgent{failCmds: map[string]bool{"db.drop": true}}
	cfg, installs, dbs, dbUsers, grants := newDeleteFixture(ag)

	runDelete(cfg)

	if dbs.deleteCount() != 0 {
		t.Errorf("databases row deleted after db.drop failed — the MariaDB schema is now an orphan nothing can name")
	}
	if dbUsers.deleteCount() != 0 {
		t.Errorf("database_users row deleted after db.drop failed")
	}
	if grants.deleteCount() != 0 {
		t.Errorf("grant rows deleted after db.drop failed — they are the record of the surviving privileges")
	}
	// The install's files are gone, so its row must still go: leaving it
	// would make the app look installed and block a re-install.
	if len(installs.deleted) != 1 {
		t.Errorf("install row should be deleted regardless (files already removed); got %d deletes", len(installs.deleted))
	}
}

func TestWordPressDelete_KeepsPanelRowsWhenUserDropFails(t *testing.T) {
	ag := &dropFailAgent{failCmds: map[string]bool{"db_user.drop": true}}
	cfg, _, dbs, dbUsers, grants := newDeleteFixture(ag)

	runDelete(cfg)

	if dbUsers.deleteCount() != 0 {
		t.Errorf("database_users row deleted after db_user.drop failed — the MySQL login survives with a valid password and no panel row")
	}
	if dbs.deleteCount() != 0 {
		t.Errorf("databases row deleted although db_user.drop failed; the pair must be kept together so the retry can reach both")
	}
	if grants.deleteCount() != 0 {
		t.Errorf("grant rows deleted after db_user.drop failed")
	}
}

func TestWordPressDelete_RemovesPanelRowsWhenDropsSucceed(t *testing.T) {
	ag := &dropFailAgent{}
	cfg, installs, dbs, dbUsers, grants := newDeleteFixture(ag)

	runDelete(cfg)

	if !ag.called("db.drop") || !ag.called("db_user.drop") {
		t.Fatalf("delete must attempt both host-side drops; calls=%v", ag.calls)
	}
	if dbs.deleteCount() != 1 {
		t.Errorf("databases row should be deleted once the drop succeeded; got %d", dbs.deleteCount())
	}
	if dbUsers.deleteCount() != 1 {
		t.Errorf("database_users row should be deleted once the drop succeeded; got %d", dbUsers.deleteCount())
	}
	if grants.deleteCount() != 1 {
		t.Errorf("grant rows should be deleted once the drops succeeded; got %d", grants.deleteCount())
	}
	if len(installs.deleted) != 1 {
		t.Errorf("install row should be deleted; got %d", len(installs.deleted))
	}
}

func TestRollbackDBChain_KeepsPanelRowsWhenDropFails(t *testing.T) {
	ag := &dropFailAgent{failCmds: map[string]bool{"db.drop": true}}
	cfg, _, dbs, dbUsers, grants := newDeleteFixture(ag)

	rollbackDBChain(context.Background(), cfg, provisionedDB{
		DBID:       "db-1",
		DBName:     "alice_wp_abc123",
		DBUserID:   "dbuser-1",
		DBUsername: "alice_wp_def456",
		GrantID:    "grant-1",
	})

	if dbs.deleteCount() != 0 || dbUsers.deleteCount() != 0 || grants.deleteCount() != 0 {
		t.Errorf("install rollback deleted panel rows after a failed drop; the freshly created MariaDB objects are now unreferenced")
	}
}

// --- clone rollback (cloneCore) ---
//
// The clone provisions the MariaDB database + user through the agent BEFORE
// it writes the install row. When that write fails, the unwind has to take
// the host side with it — and stop short of deleting the panel rows if it
// couldn't.

type cloneInstallRepo struct {
	repository.ApplicationInstallRepository
	source *models.WordPressInstall
}

func (r *cloneInstallRepo) FindByIDAndUserID(_ context.Context, _, _ string) (*models.WordPressInstall, error) {
	return r.source, nil
}

func (r *cloneInstallRepo) FindByDomainAndSubdirectory(_ context.Context, _, _ string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

func (r *cloneInstallRepo) Create(_ context.Context, _ *models.WordPressInstall) error {
	return errors.New("install row write failed")
}

type cloneDomainRepo struct {
	repository.DomainRepository
	userID string
}

func (r *cloneDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	return &models.Domain{ID: id, UserID: r.userID, Name: "dest.example.com"}, nil
}

type cloneUserRepo struct {
	repository.UserRepository
}

func (r *cloneUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	uname := "alice"
	return &models.User{ID: id, Username: &uname}, nil
}

func (r *fakeDBRepo) Create(_ context.Context, _ *models.Database) error { return nil }

func (r *fakeDBUserRepo) Create(_ context.Context, _ *models.DatabaseUser) error { return nil }

func (r *fakeGrantRepo) Create(_ context.Context, _ *models.DatabaseUserGrant) error { return nil }

func runCloneRollback(t *testing.T, ag *dropFailAgent) (*fakeDBRepo, *fakeDBUserRepo, *fakeGrantRepo) {
	t.Helper()
	dbs := &fakeDBRepo{}
	dbUsers := &fakeDBUserRepo{}
	grants := &fakeGrantRepo{}
	h := &wordPressHandler{cfg: ApplicationHandlerConfig{
		ApplicationInstalls: &cloneInstallRepo{source: &models.WordPressInstall{ID: "src-1", UserID: "user-1", DomainID: "dom-src"}},
		Domains:             &cloneDomainRepo{userID: "user-1"},
		Users:               &cloneUserRepo{},
		Databases:           dbs,
		DatabaseUsers:       dbUsers,
		DatabaseGrants:      grants,
		Agent:               ag,
	}}
	if _, err := h.cloneCore(context.Background(), "src-1", "dom-dest", false, "user-1", false); err == nil {
		t.Fatal("expected the clone to fail on the install-row write")
	}
	return dbs, dbUsers, grants
}

func TestCloneRollback_KeepsPanelRowsWhenDropFails(t *testing.T) {
	dbs, dbUsers, grants := runCloneRollback(t, &dropFailAgent{failCmds: map[string]bool{"db.drop": true}})

	if dbs.deleteCount() != 0 || dbUsers.deleteCount() != 0 || grants.deleteCount() != 0 {
		t.Errorf("clone rollback deleted panel rows after a failed drop; the database and login it just created are now unreferenced")
	}
}

func TestCloneRollback_RemovesPanelRowsWhenDropsSucceed(t *testing.T) {
	ag := &dropFailAgent{}
	dbs, dbUsers, grants := runCloneRollback(t, ag)

	if !ag.called("db.drop") || !ag.called("db_user.drop") {
		t.Fatalf("clone rollback must unwind the MariaDB side; calls=%v", ag.calls)
	}
	if dbs.deleteCount() != 1 || dbUsers.deleteCount() != 1 || grants.deleteCount() != 1 {
		t.Errorf("clone rollback must clear the panel rows once the host side is gone; db=%d user=%d grant=%d",
			dbs.deleteCount(), dbUsers.deleteCount(), grants.deleteCount())
	}
}

func TestRollbackDBChain_RemovesPanelRowsWhenDropsSucceed(t *testing.T) {
	ag := &dropFailAgent{}
	cfg, _, dbs, dbUsers, grants := newDeleteFixture(ag)

	rollbackDBChain(context.Background(), cfg, provisionedDB{
		DBID:       "db-1",
		DBName:     "alice_wp_abc123",
		DBUserID:   "dbuser-1",
		DBUsername: "alice_wp_def456",
		GrantID:    "grant-1",
	})

	if dbs.deleteCount() != 1 || dbUsers.deleteCount() != 1 || grants.deleteCount() != 1 {
		t.Errorf("install rollback must clear the panel rows once the host side is gone; db=%d user=%d grant=%d",
			dbs.deleteCount(), dbUsers.deleteCount(), grants.deleteCount())
	}
}
