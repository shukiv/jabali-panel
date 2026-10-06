package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a bundle from an uploaded file (Deps.Untrusted) can't claim
// admin-level things: server-level docker apps and admin-only custom nginx
// directives are not restored from it. And no restore binds a docker app to a
// data directory another account's app already uses.

type utDocker struct {
	repository.DockerAppRepository
	existing []*models.DockerApp
	created  []*models.DockerApp
}

func (r *utDocker) ListAll(context.Context) ([]*models.DockerApp, error) { return r.existing, nil }
func (r *utDocker) Create(_ context.Context, a *models.DockerApp) error {
	r.created = append(r.created, a)
	return nil
}
func (r *utDocker) CreatePort(context.Context, *models.DockerAppPublishedPort) error { return nil }

func createdApps(r *utDocker) map[string]bool {
	out := map[string]bool{}
	for _, a := range r.created {
		out[a.ID] = true
	}
	return out
}

func TestApply_UploadedBackupRestoresNoServerLevelDockerApp(t *testing.T) {
	dockers := &utDocker{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", ServerLevel: true},
			{ID: "ten-1", Slug: "nextcloud"},
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers, Untrusted: true,
		RestoredDockerSlugs: map[string]bool{"jabali-sounder": true, "nextcloud": true}})

	got := createdApps(dockers)
	if got["srv-1"] || !got["ten-1"] {
		t.Fatalf("created %v, want only the account's own app ten-1", got)
	}
	if !hasError(r.Errors, "server-level app can't be restored from an uploaded backup") {
		t.Fatalf("errors %v should say why srv-1 was not restored", r.Errors)
	}
}

func TestApply_UploadedBackupDropsCustomNginxDirectives(t *testing.T) {
	for _, untrusted := range []bool{true, false} {
		pools := &ppPools{}
		doms := &ppDomains{pools: pools}
		meta := ppMeta()
		meta.PHPPools, meta.Domains[0].PHPPoolID, meta.Domains[0].Mailboxes = nil, nil, nil
		raw := "location /x { fastcgi_pass unix:/run/php/bob.sock; }"
		meta.Domains[0].NginxCustomDirectives = &raw
		deps := ppDeps(pools, doms, &dcMailboxes{})
		deps.Untrusted = untrusted

		r := Apply(context.Background(), meta, deps)

		if len(doms.created) != 1 {
			t.Fatalf("untrusted=%v: domains = %+v (errors %v), want alice.org", untrusted, doms.created, r.Errors)
		}
		got := doms.created[0].NginxCustomDirectives
		if untrusted && (got != nil || !hasError(r.Errors, "custom nginx directives not restored from an uploaded backup")) {
			t.Fatalf("uploaded backup: directives = %v, errors %v; want dropped with a note", got, r.Errors)
		}
		if !untrusted && (got == nil || *got != raw) {
			t.Fatalf("own destination: directives = %v, want kept", got)
		}
	}
}

func TestApply_DockerAppOnAnotherAccountsDataDirIsRefused(t *testing.T) {
	bob, me := "u-bob", "u1"
	dockers := &utDocker{existing: []*models.DockerApp{
		{ID: "bob-gitea", UserID: &bob, Slug: "gitea", InstanceSlug: "gitea-1"},
		{ID: "srv-kuma", Slug: "uptime-kuma"}, // server-level, legacy empty instance slug
		{ID: "mine", UserID: &me, Slug: "n8n", InstanceSlug: "n8n-1"},
	}}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "a1", Slug: "gitea", InstanceSlug: "gitea-1"},
			{ID: "a2", Slug: "uptime-kuma"},
			{ID: "a3", Slug: "n8n", InstanceSlug: "n8n-2"},
			{ID: "mine", Slug: "n8n", InstanceSlug: "n8n-1"}, // the account's own app, restored over itself
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers})

	got := createdApps(dockers)
	if got["a1"] || got["a2"] {
		t.Fatalf("created %v: an app was bound to another account's data dir", got)
	}
	if !got["a3"] {
		t.Fatalf("created %v (errors %v): a new instance slug should restore", got, r.Errors)
	}
	if !hasError(r.Errors, `docker_app a1: not restored: another account's app already uses "gitea-1"`) ||
		!hasError(r.Errors, `docker_app a2: not restored: another account's app already uses "uptime-kuma"`) {
		t.Fatalf("errors %v should name both refusals", r.Errors)
	}
	if hasError(r.Errors, "docker_app mine:") {
		t.Fatalf("errors %v: the account's own app must not be refused", r.Errors)
	}
}

// utDBs / utDBUsers keep the rows Apply creates and answer List with the rows
// other accounts already have.
type utDBs struct {
	repository.DatabaseRepository
	existing []models.Database
	created  []string
}

func (r *utDBs) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	return r.existing, int64(len(r.existing)), nil
}
func (r *utDBs) Create(_ context.Context, d *models.Database) error {
	r.created = append(r.created, d.Name)
	return nil
}

type utDBUsers struct {
	repository.DatabaseUserRepository
	existing []models.DatabaseUser
	created  []string
}

func (r *utDBUsers) List(context.Context, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return r.existing, int64(len(r.existing)), nil
}
func (r *utDBUsers) Create(_ context.Context, u *models.DatabaseUser) error {
	r.created = append(r.created, u.Username)
	return nil
}

func utDBMeta(dbs, users []string) *internalbackup.AccountMetadata {
	alice := "alice"
	m := &internalbackup.AccountMetadata{User: internalbackup.MetadataUser{ID: "u1", Username: &alice}}
	for i, n := range dbs {
		m.Databases = append(m.Databases, internalbackup.MetadataDatabase{ID: "db" + string(rune('a'+i)), Name: n, Engine: "mariadb"})
	}
	for i, n := range users {
		m.DatabaseUsers = append(m.DatabaseUsers, internalbackup.MetadataDatabaseUser{ID: "du" + string(rune('a'+i)), Username: n})
	}
	return m
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

// SECURITY: a database or database-user row is a handle the account's owner
// can drop, dump, restore or re-password through the panel. No restore turns
// one of this server's own databases or MariaDB accounts into one.
func TestApply_NoRestoreRegistersThisServersOwnDatabasesOrAccounts(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		dbs, users := &utDBs{}, &utDBUsers{}
		meta := utDBMeta([]string{"jabali_panel", "mysql", "alice_wp"}, []string{"root", "jabali_panel_app", "jb_s_alice_wp", "mariadb.sys", "alice_u"})

		r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Databases: dbs, DatabaseUsers: users, Untrusted: untrusted,
			RestoredDatabases: map[string]bool{"jabali_panel": true, "mysql": true, "alice_wp": true}})

		if !sameSet(dbs.created, "alice_wp") || !sameSet(users.created, "alice_u") {
			t.Fatalf("untrusted=%v: created databases %v users %v (errors %v), want only alice_wp and alice_u", untrusted, dbs.created, users.created, r.Errors)
		}
		if !hasError(r.Errors, "database dba (jabali_panel): not restored: it is one of this server's own databases") ||
			!hasError(r.Errors, "db_user dua (root): not restored: it is one of this server's own database accounts") {
			t.Fatalf("untrusted=%v: errors %v should explain the refusals", untrusted, r.Errors)
		}
	}
}

// From an uploaded file, databases and database users stay in the account's
// own namespace (<account>_...) and never take a name another account has.
func TestApply_UploadedBackupKeepsDatabasesInTheAccountsNamespace(t *testing.T) {
	dbs := &utDBs{existing: []models.Database{{ID: "x", UserID: "u-bob", Name: "alice_shop"}}}
	users := &utDBUsers{existing: []models.DatabaseUser{{ID: "y", UserID: "u-bob", Username: "alice_admin"}}}
	meta := utDBMeta([]string{"alice_wp", "carol_x", "alice_shop", "shopdb"}, []string{"alice_u", "bob_u", "alice_admin"})

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Databases: dbs, DatabaseUsers: users, Untrusted: true,
		RestoredDatabases: map[string]bool{"alice_wp": true, "carol_x": true, "alice_shop": true, "shopdb": true}})

	if !sameSet(dbs.created, "alice_wp") || !sameSet(users.created, "alice_u") {
		t.Fatalf("created databases %v users %v (errors %v), want only alice_wp and alice_u", dbs.created, users.created, r.Errors)
	}
	for _, want := range []string{
		"database dbb (carol_x): not restored: a database from an uploaded backup must be named alice_<name>",
		"database dbc (alice_shop): not restored: another account has a database with this name",
		"db_user dub (bob_u): not restored: a database user from an uploaded backup must be named alice_<name>",
		"db_user duc (alice_admin): not restored: another account has a database user with this name",
	} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v should contain %q", r.Errors, want)
		}
	}
}

// A restore from this server's own backup destination keeps an admin-named
// database (made without the account prefix).
func TestApply_OwnDestinationKeepsAnAdminNamedDatabase(t *testing.T) {
	dbs, users := &utDBs{}, &utDBUsers{}
	meta := utDBMeta([]string{"shopdb"}, []string{"shopuser"})

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Databases: dbs, DatabaseUsers: users})

	if !sameSet(dbs.created, "shopdb") || !sameSet(users.created, "shopuser") {
		t.Fatalf("created databases %v users %v (errors %v), want both kept", dbs.created, users.created, r.Errors)
	}
}

// A docker app's slugs name its data directory and compose project: a row
// whose slug isn't a plain app name is not restored.
func TestApply_DockerAppWithAMalformedSlugIsRefused(t *testing.T) {
	dockers := &utDocker{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "bad1", Slug: "../etc"},
			{ID: "bad2", Slug: "gitea", InstanceSlug: "gitea/../../x"},
			{ID: "ok", Slug: "gitea", InstanceSlug: "gitea-2"},
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers})

	got := createdApps(dockers)
	if got["bad1"] || got["bad2"] || !got["ok"] {
		t.Fatalf("created %v (errors %v), want only ok", got, r.Errors)
	}
	if !hasError(r.Errors, `docker_app bad1: not restored: "../etc" is not an app name`) {
		t.Fatalf("errors %v should explain the refusal", r.Errors)
	}
}

// From an uploaded file, a database or docker app row is restored only for
// what the agent restored into the account (or what the account already
// has). Otherwise a row would hand the account data the agent refused: a
// database that exists here without a panel row, or an app folder another
// app left behind.
func TestApply_UploadedBackupRegistersOnlyWhatTheAgentRestored(t *testing.T) {
	me := "u1"
	dbs := &utDBs{existing: []models.Database{{ID: "x", UserID: me, Name: "alice_had"}}}
	dockers := &utDocker{existing: []*models.DockerApp{{ID: "had", UserID: &me, Slug: "n8n", InstanceSlug: "n8n-1"}}}
	meta := utDBMeta([]string{"alice_wp", "alice_orphan", "alice_had"}, nil)
	meta.DockerApps = []internalbackup.MetadataDockerApp{
		{ID: "a1", Slug: "gitea", InstanceSlug: "gitea-1"},
		{ID: "a2", Slug: "kuma"},
		{ID: "a3", Slug: "n8n", InstanceSlug: "n8n-1"},
	}

	r := Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Databases: dbs, DockerApps: dockers, Untrusted: true,
		RestoredDatabases: map[string]bool{"alice_wp": true}, RestoredDockerSlugs: map[string]bool{"gitea-1": true}})

	if !sameSet(dbs.created, "alice_wp", "alice_had") {
		t.Errorf("created databases %v (errors %v), want alice_wp and the account's own alice_had", dbs.created, r.Errors)
	}
	if got := createdApps(dockers); !got["a1"] || got["a2"] || !got["a3"] {
		t.Errorf("created apps %v (errors %v), want a1 and the account's own a3", got, r.Errors)
	}
	for _, want := range []string{
		"database dbb (alice_orphan): not restored: the restore didn't load its data into a database of this account",
		`docker_app a2: not restored: the restore didn't restore its data into an app folder of this account ("kuma")`,
	} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v should contain %q", r.Errors, want)
		}
	}
}
