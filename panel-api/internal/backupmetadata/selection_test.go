package backupmetadata

// SelectAll is the single content-selection producer for account backups
// (JAB-324). It replaced three divergent copies — the admin handler logged a
// lookup failure, the tenant handler swallowed it silently, the scheduler
// logged it. The silent copy was a real backup bug: the agent SKIPS a stage
// whose list is empty, so a nil-on-error selection dropped the whole category
// from the backup. These tests pin the consolidated behavior: a lookup failure
// is a structured Warning and the partial result that resolved is preserved,
// never silently shrunk to nil and never escalated to a fatal error.

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes (error-injecting; distinct names from the builder_* fakes) ---

type selDatabases struct {
	repository.DatabaseRepository
	rows []models.Database
	err  error
}

func (f *selDatabases) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Database, int64, error) {
	return f.rows, int64(len(f.rows)), f.err
}

type selDomains struct {
	repository.DomainRepository
	rows []models.Domain
	err  error
}

func (f *selDomains) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.rows, int64(len(f.rows)), f.err
}

// selMailboxes returns a per-domain result / error keyed by domain id, so a
// single bad domain can be simulated while its siblings succeed.
type selMailboxes struct {
	repository.MailboxRepository
	byDomain map[string][]models.Mailbox
	errFor   map[string]error
}

func (f *selMailboxes) ListByDomainID(_ context.Context, domainID string, _ repository.ListOptions) ([]models.Mailbox, int64, error) {
	if f.errFor != nil {
		if err, ok := f.errFor[domainID]; ok {
			return nil, 0, err
		}
	}
	rows := f.byDomain[domainID]
	return rows, int64(len(rows)), nil
}

type selDocker struct {
	repository.DockerAppRepository
	rows    []*models.DockerApp
	all     []*models.DockerApp
	err     error
	allErr  error
	allSeen bool
}

func (f *selDocker) ListByUserID(context.Context, string) ([]*models.DockerApp, error) {
	return f.rows, f.err
}

func (f *selDocker) ListAll(context.Context) ([]*models.DockerApp, error) {
	f.allSeen = true
	return f.all, f.allErr
}

func strp(s string) *string { return &s }

func hasWarning(warns []Warning, kind WarningKind) bool {
	for _, w := range warns {
		if w.Kind == kind {
			return true
		}
	}
	return false
}

// --- databases ---

func TestSelectAll_EngineSplit(t *testing.T) {
	d := Deps{Databases: &selDatabases{rows: []models.Database{
		{Name: "site_wp", Engine: "mariadb"},
		{Name: "legacy", Engine: ""}, // blank engine is legacy MariaDB
		{Name: "analytics", Engine: "postgres"},
		{Name: "weird", Engine: "cockroach"}, // unknown engine belongs to neither list
	}}}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(warns) != 0 {
		t.Fatalf("clean lookups must not warn, got %#v", warns)
	}
	if len(sel.MariaDB) != 2 || sel.MariaDB[0] != "site_wp" || sel.MariaDB[1] != "legacy" {
		t.Errorf("MariaDB split wrong: %#v", sel.MariaDB)
	}
	if len(sel.Postgres) != 1 || sel.Postgres[0] != "analytics" {
		t.Errorf("Postgres split wrong: %#v", sel.Postgres)
	}
}

// The core regression: a database lookup failure must surface a Warning and
// leave both engine lists empty — NOT return nil silently the way the tenant
// copy did. A silent empty made the agent skip the db stage as "owns none".
func TestSelectAll_DatabaseError_WarnsNeverSilent(t *testing.T) {
	d := Deps{Databases: &selDatabases{err: errors.New("db down")}}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(sel.MariaDB) != 0 || len(sel.Postgres) != 0 {
		t.Errorf("a lookup error must not fabricate names: %#v / %#v", sel.MariaDB, sel.Postgres)
	}
	if !hasWarning(warns, WarnDatabases) {
		t.Fatalf("a database lookup failure must be reported as a Warning, got %#v", warns)
	}
}

// --- mailboxes ---

func TestSelectAll_Mailboxes_PerDomainFailureKeepsOthers(t *testing.T) {
	d := Deps{
		Domains: &selDomains{rows: []models.Domain{{ID: "dom-a"}, {ID: "dom-b"}}},
		Mailboxes: &selMailboxes{
			byDomain: map[string][]models.Mailbox{
				"dom-b": {{EmailCached: "a@b.test"}, {EmailCached: "c@b.test"}},
			},
			errFor: map[string]error{"dom-a": errors.New("domain a mailbox list down")},
		},
	}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	// dom-b's mailboxes survive even though dom-a failed (AC5).
	if len(sel.Mailboxes) != 2 || sel.Mailboxes[0] != "a@b.test" {
		t.Errorf("a failing domain must not hide a healthy domain's mail: %#v", sel.Mailboxes)
	}
	if !hasWarning(warns, WarnMailboxes) {
		t.Fatalf("the failed domain must produce a mailbox Warning, got %#v", warns)
	}
	for _, w := range warns {
		if w.Kind == WarnMailboxes && w.Scope != "dom-a" {
			t.Errorf("mailbox Warning scope should be the failed domain id, got %q", w.Scope)
		}
	}
}

func TestSelectAll_DomainsError_Warns(t *testing.T) {
	d := Deps{
		Domains:   &selDomains{err: errors.New("domains down")},
		Mailboxes: &selMailboxes{},
	}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(sel.Mailboxes) != 0 {
		t.Errorf("a domains failure yields no mailboxes: %#v", sel.Mailboxes)
	}
	if !hasWarning(warns, WarnDomains) {
		t.Fatalf("a domains lookup failure must warn, got %#v", warns)
	}
}

// --- docker ---

func TestSelectAll_DockerUsesEffectiveSlug(t *testing.T) {
	d := Deps{DockerApps: &selDocker{rows: []*models.DockerApp{
		{ID: "app-1", Slug: "uptime-kuma", InstanceSlug: "uptime-kuma-2"}, // second instance
		{ID: "app-2", Slug: "gitea"},                                      // no instance slug → Slug
		{ID: "app-3", Slug: ""},                                           // empty → dropped (would be the whole root)
		nil,                                                               // nil row → skipped
	}}}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(warns) != 0 {
		t.Fatalf("clean docker lookup must not warn, got %#v", warns)
	}
	if len(sel.DockerApps) != 2 || sel.DockerApps[0] != "uptime-kuma-2" || sel.DockerApps[1] != "gitea" {
		t.Fatalf("docker slugs must use EffectiveSlug and drop empties: %#v", sel.DockerApps)
	}
}

func TestSelectAll_DockerError_WarnsOnly(t *testing.T) {
	d := Deps{
		Databases:  &selDatabases{rows: []models.Database{{Name: "keep", Engine: "mariadb"}}},
		DockerApps: &selDocker{err: errors.New("docker repo down")},
	}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(sel.DockerApps) != 0 {
		t.Errorf("a docker failure yields no slugs: %#v", sel.DockerApps)
	}
	if len(sel.MariaDB) != 1 {
		t.Errorf("a docker failure must not affect the database selection: %#v", sel.MariaDB)
	}
	if !hasWarning(warns, WarnDockerApps) {
		t.Fatalf("a docker lookup failure must warn, got %#v", warns)
	}
}

// GH #1360: an ADMIN account folds in the live server-level docker apps
// (UserID NULL); tenant-owned rows and deleted tombstones in ListAll are
// excluded.
func TestSelectAll_AdminIncludesServerLevel(t *testing.T) {
	repo := &selDocker{
		rows: []*models.DockerApp{{ID: "own-1", Slug: "gitea"}}, // admin's own apps
		all: []*models.DockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", InstanceSlug: "jabali-sounder-test", UserID: nil},
			{ID: "srv-2", Slug: "vaultwarden", UserID: nil},
			{ID: "ten-1", Slug: "nextcloud", UserID: strp("someone")},                        // tenant-owned
			{ID: "del-1", Slug: "ghost", UserID: nil, Status: models.DockerAppStatusDeleted}, // tombstone
		},
	}
	sel, warns := SelectAll(context.Background(), Deps{DockerApps: repo}, "admin-1", true)
	if len(warns) != 0 {
		t.Fatalf("clean admin docker lookup must not warn, got %#v", warns)
	}
	want := map[string]bool{"gitea": true, "jabali-sounder-test": true, "vaultwarden": true}
	if len(sel.DockerApps) != len(want) {
		t.Fatalf("admin docker slug set = %#v, want %v", sel.DockerApps, want)
	}
	for _, s := range sel.DockerApps {
		if !want[s] {
			t.Errorf("unexpected slug %q (tenant-owned or deleted apps must not be included)", s)
		}
	}
}

func TestSelectAll_NonAdminExcludesServerLevel(t *testing.T) {
	repo := &selDocker{
		rows: []*models.DockerApp{{ID: "own-1", Slug: "nextcloud"}},
		all:  []*models.DockerApp{{ID: "srv-1", Slug: "jabali-sounder", UserID: nil}},
	}
	sel, _ := SelectAll(context.Background(), Deps{DockerApps: repo}, "user-1", false)
	if len(sel.DockerApps) != 1 || sel.DockerApps[0] != "nextcloud" {
		t.Fatalf("non-admin must see only its own apps: %#v", sel.DockerApps)
	}
	if repo.allSeen {
		t.Errorf("ListAll must not be consulted for a non-admin selection")
	}
}

func TestSelectAll_ServerLevelListAllError_Warns(t *testing.T) {
	repo := &selDocker{
		rows:   []*models.DockerApp{{ID: "own-1", Slug: "gitea"}},
		allErr: errors.New("list all down"),
	}
	sel, warns := SelectAll(context.Background(), Deps{DockerApps: repo}, "admin-1", true)
	// the admin's own app still resolves; only the server-level fold-in failed.
	if len(sel.DockerApps) != 1 || sel.DockerApps[0] != "gitea" {
		t.Errorf("a server-level failure must keep the admin's own apps: %#v", sel.DockerApps)
	}
	if !hasWarning(warns, WarnServerDockerApps) {
		t.Fatalf("a server-level docker failure must warn, got %#v", warns)
	}
}

// --- wiring edges ---

// A nil repo is legitimate wiring (a deployment without that surface). It must
// yield no names AND no warning — the same outcome as an account that owns none.
func TestSelectAll_NilRepos_NoWarnings(t *testing.T) {
	sel, warns := SelectAll(context.Background(), Deps{}, "user-1", true)
	if len(sel.MariaDB) != 0 || len(sel.Postgres) != 0 || len(sel.Mailboxes) != 0 || len(sel.DockerApps) != 0 {
		t.Errorf("nil repos must select nothing: %#v", sel)
	}
	if len(warns) != 0 {
		t.Fatalf("nil repos are legitimate wiring, not a failure: %#v", warns)
	}
}

// The canonical fixture: one Deps drives the full four-category selection. This
// is the payload every adapter now emits, since admin / tenant / scheduler all
// call this same function with the same Deps (AC1: identical payload).
func TestSelectAll_FullFixture(t *testing.T) {
	d := Deps{
		Databases: &selDatabases{rows: []models.Database{
			{Name: "wp", Engine: "mariadb"}, {Name: "metrics", Engine: "postgres"},
		}},
		DockerApps: &selDocker{rows: []*models.DockerApp{{ID: "a", Slug: "nextcloud"}}},
		Domains:    &selDomains{rows: []models.Domain{{ID: "dom-1"}}},
		Mailboxes:  &selMailboxes{byDomain: map[string][]models.Mailbox{"dom-1": {{EmailCached: "u@dom.test"}}}},
	}
	sel, warns := SelectAll(context.Background(), d, "user-1", false)
	if len(warns) != 0 {
		t.Fatalf("fixture is all-clean: %#v", warns)
	}
	if len(sel.MariaDB) != 1 || sel.MariaDB[0] != "wp" ||
		len(sel.Postgres) != 1 || sel.Postgres[0] != "metrics" ||
		len(sel.DockerApps) != 1 || sel.DockerApps[0] != "nextcloud" ||
		len(sel.Mailboxes) != 1 || sel.Mailboxes[0] != "u@dom.test" {
		t.Fatalf("full fixture selection wrong: %#v", sel)
	}
}

func TestLogWarnings_NilLoggerNoPanic(t *testing.T) {
	// Must be a no-op, not a panic — some deployments wire a nil logger.
	LogWarnings(nil, []Warning{{Kind: WarnDatabases, Scope: "u", Err: errors.New("x")}})
}
