package backupmetadata

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a domain's web settings ride in the account backup and come back
// with the domain, through the restore checks.

// wsDomains records the domains Apply creates and the updates it makes.
type wsDomains struct {
	repository.DomainRepository
	created []models.Domain
	updated []models.Domain
}

func (r *wsDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *wsDomains) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *wsDomains) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, *d)
	return nil
}
func (r *wsDomains) Update(_ context.Context, d *models.Domain) error {
	r.updated = append(r.updated, *d)
	return nil
}

func wsJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wsDomain is a backup's domain row with every web setting set away from its
// default, made on a server where the account was olduser.
func wsDomain(t *testing.T) internalbackup.MetadataDomain {
	off := false
	return internalbackup.MetadataDomain{
		ID: "d1", Name: "shop.org", DocRoot: "/home/olduser/domains/shop.org/public_html",
		NginxRules: wsJSON(t, models.NginxRules{
			{Type: "rewrite", Pattern: "^/old$", Replacement: "/new", Flag: "permanent"},
			{Type: "static_alias", Path: "/static/", Target: "/home/olduser/app/static/"},
		}),
		PageRedirects: wsJSON(t, models.PageRedirects{
			{Source: "/promo", Destination: "https://shop.org/sale", Type: "301"},
		}),
		NginxTenantDirectives: odPtr("expires 1h;"),
		NginxSafeOptions:      wsJSON(t, models.NginxSafeOptions{MaxBodyMB: 64, HSTS: true}),
		EnvVars:               wsJSON(t, models.DomainEnvVars{{Key: "APP_ENV", Value: "production"}}),
		CacheEnabled:          true, CachePath: "/blog", CacheTTLSeconds: 1200, CacheQueryAllowlist: "paged",
		CreateWWW: &off, WebmailEnabled: &off,
		TempURLEnabled: true, BotChallengeInclude: true, AllowSubdomainDelegation: true,
		WebDisabled: true, DNSDisabled: true,
	}
}

func wsMeta(dm internalbackup.MetadataDomain) *internalbackup.AccountMetadata {
	bundleUser := "olduser"
	return &internalbackup.AccountMetadata{
		User:    internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &bundleUser},
		Domains: []internalbackup.MetadataDomain{dm},
	}
}

// passCheck accepts every row and records what it was given.
func passCheck(seen *[]models.Domain) func(context.Context, *models.Domain, string) ([]string, error) {
	return func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		*seen = append(*seen, *row)
		return nil, nil
	}
}

func TestApply_RestoresDomainWebSettings(t *testing.T) {
	doms := &wsDomains{}
	var checked []models.Domain
	r := Apply(context.Background(), wsMeta(wsDomain(t)), Deps{
		Users: namedUsersRepo{username: "alice"}, Domains: doms, CheckDomain: passCheck(&checked),
	})
	if len(doms.created) != 1 {
		t.Fatalf("created %d domains, want 1; errors: %v", len(doms.created), r.Errors)
	}
	got := doms.created[0]
	if len(checked) != 1 || len(checked[0].NginxRules) != 2 || len(checked[0].PageRedirects) != 1 {
		t.Fatalf("the restore checks saw rules %v and redirects %v; they must see the backup's", checked[0].NginxRules, checked[0].PageRedirects)
	}
	if len(got.NginxRules) != 2 || got.NginxRules[0].Pattern != "^/old$" {
		t.Fatalf("nginx rules = %+v, want the backup's two", got.NginxRules)
	}
	if want := "/home/alice/app/static/"; got.NginxRules[1].Target != want {
		t.Errorf("static alias folder = %q, want it moved onto the account's home %q", got.NginxRules[1].Target, want)
	}
	if len(got.PageRedirects) != 1 || got.PageRedirects[0].Destination != "https://shop.org/sale" {
		t.Errorf("page redirects = %+v, want the backup's", got.PageRedirects)
	}
	if got.NginxTenantDirectives == nil || *got.NginxTenantDirectives != "expires 1h;" {
		t.Errorf("advanced directives = %v, want the backup's", got.NginxTenantDirectives)
	}
	if got.NginxSafeOptions.MaxBodyMB != 64 || !got.NginxSafeOptions.HSTS {
		t.Errorf("nginx options = %+v, want the backup's", got.NginxSafeOptions)
	}
	if len(got.EnvVars) != 1 || got.EnvVars[0].Key != "APP_ENV" {
		t.Errorf("environment variables = %+v, want the backup's", got.EnvVars)
	}
	if !got.CacheEnabled || got.CachePath != "/blog" || got.CacheTTLSeconds != 1200 || got.CacheQueryAllowlist != "paged" {
		t.Errorf("page cache = %v %q %d %q, want the backup's", got.CacheEnabled, got.CachePath, got.CacheTTLSeconds, got.CacheQueryAllowlist)
	}
	if got.CreateWWW || got.WebmailEnabled {
		t.Errorf("www %v, webmail %v: want both off, as in the backup", got.CreateWWW, got.WebmailEnabled)
	}
	if !got.TempURLEnabled || !got.BotChallengeInclude || !got.AllowSubdomainDelegation || !got.WebDisabled || !got.DNSDisabled {
		t.Errorf("switches = %+v, want the backup's", got)
	}
	// The webmail column defaults on: the insert alone would leave it on.
	if len(doms.updated) != 1 || doms.updated[0].WebmailEnabled {
		t.Errorf("updates after create = %d; want one writing webmail off", len(doms.updated))
	}
}

// An archive from before the web settings keeps www and webmail on, and the
// domain needs no second write.
func TestApply_OldArchiveKeepsWWWAndWebmailOn(t *testing.T) {
	doms := &wsDomains{}
	var checked []models.Domain
	dm := internalbackup.MetadataDomain{ID: "d1", Name: "shop.org", DocRoot: "/home/olduser/domains/shop.org/public_html"}
	Apply(context.Background(), wsMeta(dm), Deps{
		Users: namedUsersRepo{username: "olduser"}, Domains: doms, CheckDomain: passCheck(&checked),
	})
	if len(doms.created) != 1 {
		t.Fatalf("created %d domains, want 1", len(doms.created))
	}
	if got := doms.created[0]; !got.CreateWWW || !got.WebmailEnabled {
		t.Errorf("www %v, webmail %v: want both on", got.CreateWWW, got.WebmailEnabled)
	}
	if len(doms.updated) != 0 {
		t.Errorf("updates after create = %d, want none", len(doms.updated))
	}
}

// A rule list or redirect list the restore can't read is left out with a
// line in the report, and the domain is still restored.
func TestApply_UnreadableRulesAreReported(t *testing.T) {
	doms := &wsDomains{}
	var checked []models.Domain
	dm := wsDomain(t)
	dm.NginxRules, dm.PageRedirects = "{not json", "[1,2"
	r := Apply(context.Background(), wsMeta(dm), Deps{
		Users: namedUsersRepo{username: "olduser"}, Domains: doms, CheckDomain: passCheck(&checked),
	})
	if len(doms.created) != 1 || len(doms.created[0].NginxRules) != 0 || len(doms.created[0].PageRedirects) != 0 {
		t.Fatalf("created %+v, want the domain without rules or redirects", doms.created)
	}
	joined := strings.Join(r.Errors, "\n")
	for _, want := range []string{"nginx rules not restored", "page redirects not restored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report lacks %q:\n%s", want, joined)
		}
	}
}

// The backup carries the domain's web settings.
func TestBuild_CarriesDomainWebSettings(t *testing.T) {
	dom := models.Domain{
		NginxTenantDirectives: odPtr("expires 1h;"),
		NginxSafeOptions:      models.NginxSafeOptions{MaxBodyMB: 64},
		EnvVars:               models.DomainEnvVars{{Key: "APP_ENV", Value: "production"}},
		CacheEnabled:          true, CachePath: "/blog", CacheTTLSeconds: 1200, CacheQueryAllowlist: "paged",
		CreateWWW: false, WebmailEnabled: false, TempURLEnabled: true,
		BotChallengeExempt: true, BotChallengeInclude: true, AllowSubdomainDelegation: true,
		WebDisabled: true, DNSDisabled: true,
	}
	dom.ID, dom.Name, dom.DocRoot = "d1", "shop.org", "/home/alice/domains/shop.org/public_html"
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{Domains: &fDomains{rows: []models.Domain{dom}}})
	if len(m.Domains) != 1 {
		t.Fatalf("backup has %d domains, want 1", len(m.Domains))
	}
	dm := m.Domains[0]
	var back models.Domain
	if p := setRestoredWebSettings(&back, dm, "alice", "alice"); len(p) != 0 {
		t.Fatalf("problems reading back: %v", p)
	}
	back.ID, back.Name, back.DocRoot = dom.ID, dom.Name, dom.DocRoot
	if wsJSON(t, back) != wsJSON(t, dom) {
		t.Errorf("round trip lost settings:\n got  %s\n want %s", wsJSON(t, back), wsJSON(t, dom))
	}
}

// wsOverwriteDomains adds the setters the environment and page-cache pages
// use to odDomains.
type wsOverwriteDomains struct{ *odDomains }

func (r wsOverwriteDomains) UpdateEnvVars(_ context.Context, id string, v models.DomainEnvVars) error {
	r.writes = append(r.writes, "env "+id)
	d := r.rows[id]
	d.EnvVars = v
	r.rows[id] = d
	return nil
}

func (r wsOverwriteDomains) UpdateCacheEnabled(_ context.Context, id string, on bool) error {
	d := r.rows[id]
	d.CacheEnabled = on
	r.rows[id] = d
	return nil
}

func (r wsOverwriteDomains) UpdateCachePath(_ context.Context, id, p string) error {
	d := r.rows[id]
	d.CachePath = p
	r.rows[id] = d
	return nil
}

func (r wsOverwriteDomains) UpdateCacheTTL(_ context.Context, id string, s int) error {
	d := r.rows[id]
	d.CacheTTLSeconds = s
	r.rows[id] = d
	return nil
}

func (r wsOverwriteDomains) UpdateCacheQueryAllowlist(_ context.Context, id, csv string) error {
	d := r.rows[id]
	d.CacheQueryAllowlist = csv
	r.rows[id] = d
	return nil
}

// wsOverwriteSetup is odSetup with the account's domain carrying its own web
// settings, and checks that also refuse the bot-challenge opt-out, as they do
// for an uploaded file.
func wsOverwriteSetup(t *testing.T) *odFixture {
	f := odSetup("d-own", true, "")
	own := f.domains.rows["d-own"]
	own.NginxRules = models.NginxRules{{Type: "proxy_pass", Path: "/app/", Target: "http://127.0.0.1:3000"}}
	own.EnvVars = models.DomainEnvVars{{Key: "OLD", Value: "1"}}
	own.CacheTTLSeconds, own.CachePath, own.WebmailEnabled = 600, "/", true
	f.domains.rows["d-own"] = own
	f.deps.Domains = wsOverwriteDomains{f.domains}
	inner := f.deps.CheckDomain
	f.deps.CheckDomain = func(ctx context.Context, row *models.Domain, owner string) ([]string, error) {
		w, err := inner(ctx, row, owner)
		// The real checks would drop the domain's own admin rules here,
		// with lines about rules the overwrite doesn't touch.
		if len(row.NginxRules) > 0 {
			w = append(w, "checked nginx rules the overwrite keeps")
		}
		if row.BotChallengeExempt {
			row.BotChallengeExempt = false
			w = append(w, "bot challenge opt-out not restored")
		}
		return w, err
	}
	return f
}

// With "Overwrite existing items with the backup", the account's domain takes
// the backup's web settings, except its typed nginx rules and what the
// checks refuse.
func TestOverwrite_DomainTakesBackupWebSettings(t *testing.T) {
	f := wsOverwriteSetup(t)
	m := odMeta("d-own")
	web := wsDomain(t)
	dm := &m.Domains[0]
	dm.NginxRules, dm.PageRedirects = web.NginxRules, web.PageRedirects
	dm.NginxTenantDirectives, dm.NginxSafeOptions, dm.EnvVars = web.NginxTenantDirectives, web.NginxSafeOptions, web.EnvVars
	dm.CacheEnabled, dm.CachePath, dm.CacheTTLSeconds, dm.CacheQueryAllowlist = true, "/blog", 1200, "paged"
	dm.WebmailEnabled, dm.TempURLEnabled = web.WebmailEnabled, true
	dm.BotChallengeExempt, dm.BotChallengeInclude, dm.AllowSubdomainDelegation = true, true, true

	r := Apply(context.Background(), m, f.deps)
	got := f.own("d-own")
	if len(got.NginxRules) != 1 || got.NginxRules[0].Type != "proxy_pass" {
		t.Errorf("nginx rules = %+v, want the domain's own", got.NginxRules)
	}
	joined := strings.Join(r.Errors, "\n")
	for _, want := range []string{"nginx rules not updated", "bot challenge opt-out not restored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "checked nginx rules the overwrite keeps") {
		t.Errorf("the checks ran on the domain's own nginx rules:\n%s", joined)
	}
	if len(got.PageRedirects) != 1 || got.NginxTenantDirectives == nil || got.NginxSafeOptions.MaxBodyMB != 64 {
		t.Errorf("redirects %+v, directives %v, options %+v: want the backup's", got.PageRedirects, got.NginxTenantDirectives, got.NginxSafeOptions)
	}
	if len(got.EnvVars) != 1 || got.EnvVars[0].Key != "APP_ENV" {
		t.Errorf("environment variables = %+v, want the backup's", got.EnvVars)
	}
	if !got.CacheEnabled || got.CachePath != "/blog" || got.CacheTTLSeconds != 1200 || got.CacheQueryAllowlist != "paged" {
		t.Errorf("page cache = %v %q %d %q, want the backup's", got.CacheEnabled, got.CachePath, got.CacheTTLSeconds, got.CacheQueryAllowlist)
	}
	if got.WebmailEnabled || !got.TempURLEnabled || !got.BotChallengeInclude || !got.AllowSubdomainDelegation {
		t.Errorf("switches = webmail %v preview %v include %v delegation %v, want the backup's",
			got.WebmailEnabled, got.TempURLEnabled, got.BotChallengeInclude, got.AllowSubdomainDelegation)
	}
	if got.BotChallengeExempt {
		t.Error("bot challenge opt-out set from an uploaded file")
	}
}

// An archive from before the web settings leaves the domain's own alone.
func TestOverwrite_OldArchiveKeepsDomainWebSettings(t *testing.T) {
	f := wsOverwriteSetup(t)
	m := odMeta("d-own")
	before := f.own("d-own")
	r := Apply(context.Background(), m, f.deps)
	got := f.own("d-own")
	if !sameJSON(got.EnvVars, before.EnvVars) || got.CacheTTLSeconds != before.CacheTTLSeconds || !got.WebmailEnabled {
		t.Errorf("web settings changed by an archive that has none: %+v", got)
	}
	if strings.Contains(strings.Join(r.Errors, "\n"), "nginx rules not updated") {
		t.Errorf("report names the rules of an archive that has no web settings:\n%s", strings.Join(r.Errors, "\n"))
	}
}
