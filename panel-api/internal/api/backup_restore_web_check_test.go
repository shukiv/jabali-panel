package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a restored domain's web settings pass the rules of the page that
// sets each one. From an uploaded file only what the account's owner could
// set comes back.

type rdcPreviews []models.Domain

func (p rdcPreviews) ListPreviewEnabled(context.Context) ([]models.Domain, error) { return p, nil }

// rdcOwnerOptions is a server that lets account owners set their domains'
// nginx options (tenant_domain_options_enabled), or doesn't.
type rdcOwnerOptions bool

func (o rdcOwnerOptions) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: "panel.example.net", TenantDomainOptionsEnabled: bool(o)}, nil
}

func webCheck(source RestoreSource, previews rdcPreviews) func(context.Context, *models.Domain, string) ([]string, error) {
	return RestoreDomainCheck(rdcDomains{}, rdcAliases{}, rdcOwnerOptions(true), previews, source)
}

func webRules() models.NginxRules {
	return models.NginxRules{
		{Type: "rewrite", Pattern: "^/old$", Replacement: "/new"},
		{Type: "proxy_pass", Path: "/api/", Target: "https://api.example.net"},
		{Type: "custom_header", Name: "X-Shop", Value: "1"},
		{Type: "static_alias", Path: "/static/", Target: "/home/alice/app/static/"},
	}
}

func ruleTypes(rules models.NginxRules) string {
	var out []string
	for _, r := range rules {
		out = append(out, r.Type)
	}
	return strings.Join(out, ",")
}

func TestRestoreWebCheck_OwnBackupKeepsAdminRules(t *testing.T) {
	row := rdcRow("shop.org")
	row.NginxRules = webRules()
	w, err := webCheck(RestoreFromOwnBackup, nil)(context.Background(), row, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleTypes(row.NginxRules); got != "rewrite,proxy_pass,custom_header,static_alias" {
		t.Errorf("rules = %s, want all four; warnings: %v", got, w)
	}
}

func TestRestoreWebCheck_UploadKeepsOnlyTenantRules(t *testing.T) {
	row := rdcRow("shop.org")
	row.NginxRules = webRules()
	w, err := webCheck(RestoreFromUpload, nil)(context.Background(), row, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleTypes(row.NginxRules); got != "rewrite,custom_header" {
		t.Errorf("rules = %s, want rewrite,custom_header", got)
	}
	joined := strings.Join(w, "\n")
	for _, want := range []string{"nginx rule 2 (proxy_pass) dropped: only an administrator", "nginx rule 4 (static_alias) dropped: only an administrator"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
}

// A rule the rule builder would refuse is dropped, each with its reason, and
// the rest are kept in order.
func TestRestoreWebCheck_DropsRulesTheBuilderRefuses(t *testing.T) {
	row := rdcRow("shop.org")
	row.NginxRules = models.NginxRules{
		{Type: "proxy_pass", Path: "/app/", Target: "http://127.0.0.1:3000"},
		{Type: "static_alias", Path: "/s/", Target: "/home/bob/secrets/"},
		{Type: "front_controller", Script: "/index.php"},
		{Type: "front_controller", Script: "/other.php"},
		{Type: "teleport"},
		{Type: "rewrite", Pattern: "^/a$", Replacement: "/b"},
	}
	w, _ := webCheck(RestoreFromOwnBackup, nil)(context.Background(), row, "alice")
	if got := ruleTypes(row.NginxRules); got != "front_controller,rewrite" {
		t.Errorf("rules = %s, want front_controller,rewrite; warnings: %v", got, w)
	}
	joined := strings.Join(w, "\n")
	for _, want := range []string{
		"nginx rule 1 (proxy_pass) dropped: target may not point at an internal address",
		"nginx rule 2 (static_alias) dropped: its folder /home/bob/secrets/ is outside the account's home",
		"nginx rule 4 (front_controller) dropped: a domain can have only one",
		"nginx rule 5 (teleport) dropped: unknown type",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
}

// From an uploaded file a tenant rule still has to pass the tenant rules.
func TestRestoreWebCheck_UploadHoldsTenantRulesToTenantLimits(t *testing.T) {
	row := rdcRow("shop.org")
	row.NginxRules = models.NginxRules{
		{Type: "rewrite", Pattern: "^/x$", Replacement: "https://evil.example/"},
		{Type: "custom_header", Name: "Strict-Transport-Security", Value: "max-age=0"},
		{Type: "custom_header", Name: "X-Ok", Value: "1"},
	}
	w, _ := webCheck(RestoreFromUpload, nil)(context.Background(), row, "alice")
	if got := ruleTypes(row.NginxRules); got != "custom_header" || row.NginxRules[0].Name != "X-Ok" {
		t.Errorf("rules = %+v, want only X-Ok", row.NginxRules)
	}
	if len(w) != 2 {
		t.Errorf("warnings = %v, want two", w)
	}
}

func TestRestoreWebCheck_DropsWhatTheirPagesRefuse(t *testing.T) {
	row := rdcRow("shop.org")
	row.PageRedirects = models.PageRedirects{
		{Source: "no-slash", Destination: "https://shop.org/", Type: "301"},
		{Source: "/ok", Destination: "https://shop.org/new", Type: "302"},
	}
	row.NginxTenantDirectives = strp("proxy_pass http://127.0.0.1:8443;")
	row.NginxSafeOptions = models.NginxSafeOptions{MaxBodyMB: -5}
	row.EnvVars = models.DomainEnvVars{{Key: "PHP_ADMIN_VALUE", Value: "x"}, {Key: "APP_ENV", Value: "prod"}, {Key: "APP_ENV", Value: "dup"}}
	row.CachePath, row.CacheTTLSeconds, row.CacheQueryAllowlist = "/blog/../../etc", 999999, "page,Bad Name"
	row.BotChallengeExempt = true
	w, err := webCheck(RestoreFromUpload, nil)(context.Background(), row, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(row.PageRedirects) != 1 || row.PageRedirects[0].Source != "/ok" {
		t.Errorf("page redirects = %+v, want only /ok", row.PageRedirects)
	}
	if row.NginxTenantDirectives != nil {
		t.Errorf("advanced directives kept: %q", *row.NginxTenantDirectives)
	}
	if row.NginxSafeOptions != (models.NginxSafeOptions{}) {
		t.Errorf("nginx options kept: %+v", row.NginxSafeOptions)
	}
	if len(row.EnvVars) != 1 || row.EnvVars[0].Value != "prod" {
		t.Errorf("environment variables = %+v, want only APP_ENV=prod", row.EnvVars)
	}
	if row.CachePath != "/" || row.CacheTTLSeconds != 86400 || row.CacheQueryAllowlist != "" {
		t.Errorf("page cache = %q %d %q, want / 86400 and no query parameters", row.CachePath, row.CacheTTLSeconds, row.CacheQueryAllowlist)
	}
	if row.BotChallengeExempt {
		t.Error("bot challenge opt-out kept from an uploaded file")
	}
	joined := strings.Join(w, "\n")
	for _, want := range []string{"page redirect 1 (no-slash) dropped", "advanced nginx directives dropped", "nginx options dropped",
		`environment variable "PHP_ADMIN_VALUE" dropped`, `environment variable "APP_ENV" dropped: it is set twice`,
		"page cache path", "page cache query parameters dropped", "bot challenge opt-out not restored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
}

// From the server's own backup the bot-challenge opt-out comes back.
func TestRestoreWebCheck_OwnBackupKeepsBotOptOut(t *testing.T) {
	row := rdcRow("shop.org")
	row.BotChallengeExempt = true
	if _, err := webCheck(RestoreFromOwnBackup, nil)(context.Background(), row, "alice"); err != nil {
		t.Fatal(err)
	}
	if !row.BotChallengeExempt {
		t.Error("bot challenge opt-out dropped from the server's own backup")
	}
}

// A preview URL that would collide with another domain's is left off.
func TestRestoreWebCheck_PreviewURLCollision(t *testing.T) {
	other := models.Domain{ID: "d-other", Name: "shop.org", TempURLEnabled: true}
	row := rdcRow("shop.org")
	row.TempURLEnabled = true
	w, _ := webCheck(RestoreFromOwnBackup, rdcPreviews{other})(context.Background(), row, "alice")
	if row.TempURLEnabled || !strings.Contains(strings.Join(w, "\n"), "preview URL left off") {
		t.Errorf("preview on %v, warnings %v: want it left off with a warning", row.TempURLEnabled, w)
	}

	row = rdcRow("shop.org")
	row.TempURLEnabled = true
	if _, err := webCheck(RestoreFromOwnBackup, rdcPreviews{})(context.Background(), row, "alice"); err != nil || !row.TempURLEnabled {
		t.Errorf("preview on %v (err %v): want it kept when nothing collides", row.TempURLEnabled, err)
	}
}

// The restore doors check an uploaded file's settings as the account owner's,
// and a backup from the server's own destination as the administrator's.
type rwcRepoDomains struct {
	repository.DomainRepository
	rdcDomains
	rdcPreviews
}

type rwcRepoAliases struct {
	repository.WebDomainAliasRepository
	rdcAliases
}

type rwcRepoSettings struct {
	repository.ServerSettingsRepository
	rdcSettings domainops.MailSettingsReader
}

func (r rwcRepoDomains) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	return r.rdcDomains.FindByName(ctx, name)
}

func (r rwcRepoAliases) FindByHostname(ctx context.Context, host string) (*models.WebDomainAlias, error) {
	return r.rdcAliases.FindByHostname(ctx, host)
}

func (r rwcRepoSettings) Get(ctx context.Context) (*models.ServerSettings, error) {
	return r.rdcSettings.Get(ctx)
}

func (r rwcRepoDomains) FindStrictSubdomains(ctx context.Context, name string) ([]models.Domain, error) {
	return r.rdcDomains.FindStrictSubdomains(ctx, name)
}

func (r rwcRepoDomains) ListPreviewEnabled(ctx context.Context) ([]models.Domain, error) {
	return r.rdcPreviews.ListPreviewEnabled(ctx)
}

func TestRestoreMetadataDeps_ChecksByWhereTheBackupComesFrom(t *testing.T) {
	h := &backupHandler{cfg: BackupHandlerConfig{
		Domains: rwcRepoDomains{rdcDomains: rdcDomains{}}, WebDomainAliases: rwcRepoAliases{rdcAliases: rdcAliases{}},
		ServerSettings: rwcRepoSettings{rdcSettings: rdcOwnerOptions(true)},
	}}
	for _, tc := range []struct {
		name     string
		uploaded *uploadedData
		want     string
	}{
		{"own destination", nil, "rewrite,proxy_pass,custom_header,static_alias"},
		{"uploaded file", &uploadedData{}, "rewrite,custom_header"},
	} {
		row := rdcRow("shop.org")
		row.NginxRules = webRules()
		if _, err := h.restoreMetadataDeps(tc.uploaded).CheckDomain(context.Background(), row, "alice"); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := ruleTypes(row.NginxRules); got != tc.want {
			t.Errorf("%s: rules = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// While the server doesn't let account owners set their domains' nginx
// options, an uploaded file brings none of them back; a backup from the
// server's own destination still does.
func TestRestoreWebCheck_UploadNeedsOwnerDomainOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings domainops.MailSettingsReader
		source   RestoreSource
		want     string
	}{
		{"upload, owners may not", rdcOwnerOptions(false), RestoreFromUpload, ""},
		{"upload, owners may", rdcOwnerOptions(true), RestoreFromUpload, "rewrite,custom_header"},
		{"own backup, owners may not", rdcOwnerOptions(false), RestoreFromOwnBackup, "rewrite,proxy_pass,custom_header,static_alias"},
	} {
		row := rdcRow("shop.org")
		row.NginxRules = webRules()
		row.NginxSafeOptions = models.NginxSafeOptions{MaxBodyMB: 64}
		row.NginxTenantDirectives = strp("expires 1h;")
		check := RestoreDomainCheck(rdcDomains{}, rdcAliases{}, tc.settings, nil, tc.source)
		w, err := check(context.Background(), row, "alice")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := ruleTypes(row.NginxRules); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
		kept := tc.want != ""
		if (row.NginxSafeOptions.MaxBodyMB == 64) != kept || (row.NginxTenantDirectives != nil) != kept {
			t.Errorf("%s: options %+v, directives %v; want kept=%v", tc.name, row.NginxSafeOptions, row.NginxTenantDirectives, kept)
		}
		if !kept && !strings.Contains(strings.Join(w, "\n"), "not restored from an uploaded backup") {
			t.Errorf("%s: warnings lack the reason: %v", tc.name, w)
		}
	}
}

// Settings that can't be read restore none of them. (The name checks refuse
// such a domain first; this holds the drop on its own.)
func TestRestoreWebCheck_UnreadableSettingsRestoreNoDomainOptions(t *testing.T) {
	row := rdcRow("shop.org")
	row.NginxRules = webRules()
	w := dropUnlessOwnersSetDomainOptions(context.Background(), row, rdcBrokenSettings{})
	if len(row.NginxRules) != 0 || len(w) != 1 || !strings.Contains(w[0], "could not be read") {
		t.Errorf("rules %v, warnings %v: want none kept and the read error named", row.NginxRules, w)
	}
}

type rdcBrokenSettings struct{}

func (rdcBrokenSettings) Get(context.Context) (*models.ServerSettings, error) {
	return nil, errors.New("db down")
}
