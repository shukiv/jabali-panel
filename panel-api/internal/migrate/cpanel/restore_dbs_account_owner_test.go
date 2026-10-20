package cpanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A MariaDB account is server-wide, and the import writes accounts from names
// it builds (<target>_<db>) or reads from the archive (compat users). These
// tests put other panel users' accounts, and accounts no row names, on a fake
// engine and check that the import never changes, grants or drops an account
// it did not make.

const (
	ownerAlice = "u-alice"
	sourceHash = "*0123456789ABCDEF0123456789ABCDEF01234567"
	oldHash    = "*FEDCBA9876543210FEDCBA9876543210FEDCBA98"
)

// engineAgent holds MariaDB's accounts as the agent sees them (name to
// password or hash) and answers db_user.create the way the agent does.
type engineAgent struct {
	accounts map[string]string
	grants   []string // user@db
	drops    []string
}

func (a *engineAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	name, _ := p["db_user_name"].(string)
	switch command {
	case "db_user.create":
		_, exists := a.accounts[name]
		if createOnly, _ := p["create_only"].(bool); createOnly && exists {
			return nil, &agent.AgentError{Code: agent.CodeAlreadyExists, Message: "a MariaDB account with this name already exists"}
		}
		if hash, _ := p["password_hash"].(string); hash != "" {
			a.accounts[name] = hash // CREATE USER IF NOT EXISTS, then ALTER USER
		} else if !exists {
			a.accounts[name], _ = p["password"].(string) // CREATE USER IF NOT EXISTS
		}
	case "db_user.grant":
		db, _ := p["db_name"].(string)
		a.grants = append(a.grants, name+"@"+db)
	case "db_user.drop":
		a.drops = append(a.drops, name)
		delete(a.accounts, name)
	}
	return json.RawMessage(`{}`), nil
}

func (a *engineAgent) granted(user string) bool {
	for _, g := range a.grants {
		if strings.HasPrefix(g, user+"@") {
			return true
		}
	}
	return false
}

// ownerRows is database_users, with its (user_id, username) unique key.
type ownerRows struct {
	repository.DatabaseUserRepository
	rows       []models.DatabaseUser
	failCreate bool
}

func (r *ownerRows) Create(_ context.Context, du *models.DatabaseUser) error {
	if r.failCreate {
		return errors.New("insert failed")
	}
	for _, row := range r.rows {
		if row.UserID == du.UserID && row.Username == du.Username {
			return errors.New("Error 1062: Duplicate entry for key 'uniq_user_username'")
		}
	}
	r.rows = append(r.rows, *du)
	return nil
}

func (r *ownerRows) ListByEngineAndUsername(_ context.Context, engine, username string) ([]models.DatabaseUser, error) {
	var out []models.DatabaseUser
	for _, row := range r.rows {
		if row.Engine == engine && row.Username == username {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *ownerRows) CountByEngineAndUsername(ctx context.Context, engine, username string) (int64, error) {
	rows, err := r.ListByEngineAndUsername(ctx, engine, username)
	return int64(len(rows)), err
}

// named returns the rows naming username.
func (r *ownerRows) named(username string) []models.DatabaseUser {
	rows, _ := r.ListByEngineAndUsername(context.Background(), "mariadb", username)
	return rows
}

func (r *ownerRows) count(username string) int { return len(r.named(username)) }

// grantRows is database_user_grants, with its (database_id, database_user_id)
// unique key.
type grantRows struct {
	repository.DatabaseUserGrantRepository
	rows []models.DatabaseUserGrant
}

func (r *grantRows) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	if _, err := r.FindByDBAndDBUser(context.Background(), g.DatabaseID, g.DatabaseUserID); err == nil {
		return errors.New("Error 1062: Duplicate entry for key 'uniq_db_user'")
	}
	r.rows = append(r.rows, *g)
	return nil
}

func (r *grantRows) FindByDBAndDBUser(_ context.Context, dbID, duID string) (*models.DatabaseUserGrant, error) {
	for i := range r.rows {
		if r.rows[i].DatabaseID == dbID && r.rows[i].DatabaseUserID == duID {
			return &r.rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *grantRows) of(duID string) []models.DatabaseUserGrant {
	var out []models.DatabaseUserGrant
	for _, g := range r.rows {
		if g.DatabaseUserID == duID {
			out = append(out, g)
		}
	}
	return out
}

func writeDumps(t *testing.T, names ...string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("-- dump\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func skippedHas(res *DBImportResult, sub string) bool {
	for _, s := range res.Skipped {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Each imported database's own user is <target>_<db>. When a row or the
// engine already has that name, the database gets no user: no second row for
// another tenant's account, no grant of the imported data to it, and no drop
// of a login the target already uses.
func TestImportDatabases_PanelUserNeverTakesAnExistingAccount(t *testing.T) {
	// alice_x_app is tenant alice_x's; alice_blog is alice's own, in use;
	// alice_shop is on the engine with no row.
	rows := &ownerRows{rows: []models.DatabaseUser{
		{ID: "du-x", UserID: "u-alice-x", Username: "alice_x_app", Engine: "mariadb"},
		{ID: "du-blog", UserID: ownerAlice, Username: "alice_blog", Engine: "mariadb"},
	}}
	ag := &engineAgent{accounts: map[string]string{"alice_x_app": "x-pw", "alice_blog": "blog-pw", "alice_shop": "shop-pw"}}
	dir, dumps := writeDumps(t, "cp_x_app.sql", "cp_blog.sql", "cp_shop.sql", "cp_new.sql")

	res, err := ImportDatabases(context.Background(), gh723DBRepo{}, rows, gh723GrantRepo{}, ag,
		&ParsedTarball{ExtractDir: dir, SourceUser: "cp", MySQLDumps: dumps}, ownerAlice, "alice", false)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if res.Created != 4 {
		t.Fatalf("Created = %d, want 4: every database is restored (skipped=%v)", res.Created, res.Skipped)
	}
	for name, pw := range map[string]string{"alice_x_app": "x-pw", "alice_blog": "blog-pw", "alice_shop": "shop-pw"} {
		if got, ok := ag.accounts[name]; !ok || got != pw {
			t.Errorf("%s: account changed or dropped (password %q, present %v)", name, got, ok)
		}
		if ag.granted(name) {
			t.Errorf("%s was granted an imported database", name)
		}
	}
	if len(ag.drops) != 0 {
		t.Errorf("dropped %v", ag.drops)
	}
	for name, want := range map[string]int{"alice_x_app": 1, "alice_blog": 1, "alice_shop": 0, "alice_new": 1} {
		if got := rows.count(name); got != want {
			t.Errorf("%s: %d rows, want %d", name, got, want)
		}
	}
	if !ag.granted("alice_new") {
		t.Error("alice_new, a free name, got no grant")
	}
	for _, base := range []string{"cp_x_app", "cp_blog", "cp_shop"} {
		if cred := res.Credentials[base]; cred.Password != "" || cred.DBUser != "" {
			t.Errorf("%s: credential %+v for a database that got no user", base, cred)
		}
	}
	// The app config of a database with no user is left as it is: it must not
	// take alice_new's credential, the only one with a password.
	cfg := "<?php\ndefine( 'DB_NAME', 'cp_x_app' );\ndefine( 'DB_USER', 'cp_x_app' );\ndefine( 'DB_PASSWORD', 'source-pw' );\ndefine( 'DB_HOST', 'localhost' );\n"
	if out, changed := rewriteWordPress(cfg, res.Credentials); changed {
		t.Errorf("wp-config of cp_x_app rewritten:\n%s", out)
	}
	if !skippedHas(res, "alice_x_app: database user not created") {
		t.Errorf("no report line for alice_x_app: %v", res.Skipped)
	}
}

// A compat user's name and hash come from the archive. The hash goes only to
// an account whose sole row is the target's (the user this import made, GH
// #633) or to a new account: another tenant's account, an account no row
// names, and a sign-in account keep their password and get no grant.
func TestImportDatabases_CompatUserNeverTakesAnExistingAccount(t *testing.T) {
	rows := &ownerRows{rows: []models.DatabaseUser{
		{ID: "du-bob", UserID: "u-bob", Username: "bob_wp", Engine: "mariadb"},
	}}
	ag := &engineAgent{accounts: map[string]string{"bob_wp": oldHash, "carol_old": oldHash, "bob_mysqladmin": oldHash}}
	dir, dumps := writeDumps(t, "alice_wp.sql")
	compat := func(name string) CompatUser {
		return CompatUser{Name: name, Host: "localhost", Hash: sourceHash,
			Grant: []CompatGrant{{SourceDB: "alice_wp", Privs: []string{"ALL"}}}}
	}
	parsed := &ParsedTarball{ExtractDir: dir, SourceUser: "alice", MySQLDumps: dumps, CompatUsers: []CompatUser{
		compat("bob_wp"), compat("carol_old"), compat("bob_mysqladmin"), compat("alice_wp"), compat("alice_legacy"),
	}}

	grants := &grantRows{}
	res, err := ImportDatabases(context.Background(), gh723DBRepo{}, rows, grants, ag,
		parsed, ownerAlice, "alice", true)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	for _, name := range []string{"bob_wp", "carol_old", "bob_mysqladmin"} {
		if ag.accounts[name] != oldHash {
			t.Errorf("%s: password changed to %q", name, ag.accounts[name])
		}
		if ag.granted(name) {
			t.Errorf("%s was granted the imported database", name)
		}
		if res.Credentials["alice_wp"].preservesUser(name) {
			t.Errorf("%s marked preserved: the app config would keep a login that is not this account's", name)
		}
	}
	for _, name := range []string{"alice_wp", "alice_legacy"} {
		if ag.accounts[name] != sourceHash {
			t.Errorf("%s: password %q, want the source's hash", name, ag.accounts[name])
		}
		if !ag.granted(name) {
			t.Errorf("%s got no grant", name)
		}
	}
	if !res.Credentials["alice_wp"].preservesUser("alice_wp") {
		t.Errorf("alice_wp not preserved: %+v", res.Credentials["alice_wp"])
	}
	// Each account the import holds is one of alice's database users, with
	// its grant shown once: alice_wp is the user it made for alice_wp, and
	// alice_legacy is new.
	for _, name := range []string{"alice_wp", "alice_legacy"} {
		got := rows.named(name)
		if len(got) != 1 || got[0].UserID != ownerAlice {
			t.Fatalf("%s rows %+v, want one of alice's", name, got)
		}
		if g := grants.of(got[0].ID); len(g) != 1 || g[0].GrantLevel != "rw" || g[0].Privileges != "ALL" {
			t.Errorf("%s grant rows %+v, want one rw ALL", name, g)
		}
	}
	if h := rows.named("alice_legacy")[0].PasswordHash; h != "" {
		t.Errorf("alice_legacy row hash %q: the panel never knew its password", h)
	}
	for name, want := range map[string]int{"bob_wp": 1, "carol_old": 0, "bob_mysqladmin": 0} {
		if got := rows.count(name); got != want {
			t.Errorf("%s: %d rows, want %d", name, got, want)
		}
	}
	for _, want := range []string{
		"compat_user bob_wp: not recreated: a database user of this name belongs to another account",
		"compat_user carol_old: not recreated: a MariaDB account of this name already exists",
		"compat_user bob_mysqladmin: not recreated: names ending in _mysqladmin",
	} {
		if !skippedHas(res, want) {
			t.Errorf("no report line %q: %v", want, res.Skipped)
		}
	}
}

// On a resume, a compat user that already exists with no row is refused like
// any other. The credential the resume seeded then has no password and no
// preserved user, and the app config is left as it is rather than given an
// empty password.
func TestImportDatabases_ResumeRefusedCompatUserLeavesConfig(t *testing.T) {
	ag := &engineAgent{accounts: map[string]string{"notary_45635": oldHash}}
	dir, dumps := writeDumps(t, "notary_45635.mysql.sql")
	parsed := &ParsedTarball{ExtractDir: dir, SourceUser: "notary", MySQLDumps: dumps, CompatUsers: []CompatUser{{
		Name: "notary_45635", Host: "localhost", Hash: sourceHash,
		Grant: []CompatGrant{{SourceDB: "notary_45635", Privs: []string{"ALL"}}},
	}}}

	res, err := ImportDatabases(context.Background(), gh723ResumeDBRepo{}, &ownerRows{}, gh723GrantRepo{}, ag,
		parsed, ownerAlice, "sewickley", true)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if ag.accounts["notary_45635"] != oldHash || ag.granted("notary_45635") {
		t.Errorf("notary_45635 changed: password %q, grants %v", ag.accounts["notary_45635"], ag.grants)
	}
	if out, changed := rewriteWordPress(gh723PreserveWPConfig, res.Credentials); changed {
		t.Errorf("wp-config rewritten with no password to write:\n%s", out)
	}
}

// A resume after this import recorded a source user as the target's: the row
// says the account is the target's, so the import sets its hash again, grants
// it the database and keeps the app config's credentials.
func TestImportDatabases_ResumeTakesBackItsOwnCompatUser(t *testing.T) {
	rows := &ownerRows{rows: []models.DatabaseUser{
		{ID: "du-notary", UserID: ownerAlice, Username: "notary_45635", Engine: "mariadb"},
	}}
	grants := &grantRows{}
	ag := &engineAgent{accounts: map[string]string{"notary_45635": oldHash}}
	dir, dumps := writeDumps(t, "notary_45635.mysql.sql")
	parsed := &ParsedTarball{ExtractDir: dir, SourceUser: "notary", MySQLDumps: dumps, CompatUsers: []CompatUser{{
		Name: "notary_45635", Host: "localhost", Hash: sourceHash,
		Grant: []CompatGrant{{SourceDB: "notary_45635", Privs: []string{"ALL"}}},
	}}}

	res, err := ImportDatabases(context.Background(), gh723ResumeDBRepo{}, rows, grants, ag,
		parsed, ownerAlice, "sewickley", true)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if ag.accounts["notary_45635"] != sourceHash || !ag.granted("notary_45635") {
		t.Errorf("notary_45635: password %q, grants %v; want the source's hash and a grant", ag.accounts["notary_45635"], ag.grants)
	}
	if rows.count("notary_45635") != 1 {
		t.Errorf("rows %+v, want the one row", rows.named("notary_45635"))
	}
	if g := grants.of("du-notary"); len(g) != 1 || g[0].DatabaseID != "db-resumed" {
		t.Errorf("grant rows %+v, want one on the resumed database", g)
	}
	out, changed := rewriteWordPress(gh723PreserveWPConfig, res.Credentials)
	if !changed || !strings.Contains(out, "define( 'DB_PASSWORD', 'e4eb22f97bb143151c95' );") {
		t.Errorf("wp-config must keep the source credentials (changed=%v):\n%s", changed, out)
	}
}

// A source user whose row can't be written is dropped again: no login is
// left that no row names.
func TestImportDatabases_CompatUserRowFailureDropsTheLogin(t *testing.T) {
	ag := &engineAgent{accounts: map[string]string{}}
	parsed := &ParsedTarball{ExtractDir: t.TempDir(), SourceUser: "alice", CompatUsers: []CompatUser{{
		Name: "alice_legacy", Host: "localhost", Hash: sourceHash,
	}}}
	res, err := ImportDatabases(context.Background(), gh723DBRepo{}, &ownerRows{failCreate: true}, &grantRows{}, ag,
		parsed, ownerAlice, "alice", true)
	if err != nil {
		t.Fatalf("ImportDatabases: %v", err)
	}
	if _, ok := ag.accounts["alice_legacy"]; ok || len(ag.drops) != 1 {
		t.Errorf("alice_legacy left on the engine (drops %v)", ag.drops)
	}
	if !skippedHas(res, "compat_user alice_legacy: database_users row failed; MariaDB user rolled back") {
		t.Errorf("no report line: %v", res.Skipped)
	}
}

// A credential with no password, whose config user is not a preserved one,
// has nothing to write: the rewriter leaves the config as it is.
func TestRewriteWordPress_NoPasswordToWriteLeavesConfig(t *testing.T) {
	creds := map[string]DBCredential{"notary_45635": {DBName: "sewickley_45635", DBUser: "sewickley_45635"}}
	if out, changed := rewriteWordPress(gh723PreserveWPConfig, creds); changed {
		t.Errorf("wp-config rewritten:\n%s", out)
	}
}
