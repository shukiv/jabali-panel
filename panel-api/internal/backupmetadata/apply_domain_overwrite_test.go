package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: with "Overwrite existing items with the backup" checked, a domain
// the account already has takes the backup's settings, within the rules its
// own pages apply. Its name, document root, SSL, DKIM, DNSSEC, custom nginx
// directives, mail provider and ownership stay as they are.

// odDomains holds the domains on this server. It hands out copies, as the
// database does, and applies each write the way the repository does.
type odDomains struct {
	repository.DomainRepository
	rows   map[string]models.Domain
	writes []string
}

func (r *odDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := r.rows[id]; ok {
		return &d, nil
	}
	return nil, repository.ErrNotFound
}

func (r *odDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	for _, d := range r.rows {
		if d.Name == name {
			c := d
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *odDomains) Create(_ context.Context, d *models.Domain) error {
	r.writes = append(r.writes, "create "+d.ID)
	r.rows[d.ID] = *d
	return nil
}

// Update writes the whole row it is given, as the repository's column list
// does for every column a restore could get wrong.
func (r *odDomains) Update(_ context.Context, d *models.Domain) error {
	r.writes = append(r.writes, "update "+d.ID)
	r.rows[d.ID] = *d
	return nil
}

// UpdatePHPSettings writes every PHP field; nil clears it.
func (r *odDomains) UpdatePHPSettings(_ context.Context, id string, s repository.DomainPHPSettings) error {
	r.writes = append(r.writes, "php "+id)
	d := r.rows[id]
	d.PHPMemoryLimit, d.PHPUploadMaxFilesize, d.PHPPostMaxSize = s.MemoryLimit, s.UploadMaxFilesize, s.PostMaxSize
	d.PHPMaxInputVars, d.PHPMaxExecutionTime, d.PHPMaxInputTime = s.MaxInputVars, s.MaxExecutionTime, s.MaxInputTime
	d.PHPDisplayErrors, d.PHPErrorReporting, d.PHPTimezone = s.DisplayErrors, s.ErrorReporting, s.Timezone
	d.PHPLogErrors, d.PHPFileUploads, d.PHPShortOpenTag = s.LogErrors, s.FileUploads, s.ShortOpenTag
	d.PHPOpenBasedir, d.PHPAllowURLFopen = s.OpenBasedir, s.AllowURLFopen
	r.rows[id] = d
	return nil
}

func (r *odDomains) SetPHPPoolID(_ context.Context, id string, poolID *string) error {
	r.writes = append(r.writes, "pool "+id)
	d := r.rows[id]
	d.PHPPoolID = poolID
	r.rows[id] = d
	return nil
}

func (r *odDomains) SetRateLimits(_ context.Context, id string, rps, conn uint32) error {
	r.writes = append(r.writes, "rate "+id)
	d := r.rows[id]
	d.RateLimitRPS, d.ConnectionLimit = rps, conn
	r.rows[id] = d
	return nil
}

func (r *odDomains) UpdateCatchallTarget(_ context.Context, id string, target *string) error {
	r.writes = append(r.writes, "catchall "+id)
	d := r.rows[id]
	d.CatchallTarget = target
	r.rows[id] = d
	return nil
}

func (r *odDomains) UpdateDisclaimer(_ context.Context, id string, enabled bool, text *string) error {
	r.writes = append(r.writes, "disclaimer "+id)
	d := r.rows[id]
	d.DisclaimerEnabled, d.DisclaimerText = enabled, text
	r.rows[id] = d
	return nil
}

// odMailboxes holds the mailboxes on this server, by address. A mailbox the
// restore creates is found by its address from then on.
type odMailboxes struct {
	repository.MailboxRepository
	domains *odDomains
	byEmail map[string]models.Mailbox
}

func (r *odMailboxes) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	for _, mb := range r.byEmail {
		if mb.ID == id {
			c := mb
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *odMailboxes) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	if mb, ok := r.byEmail[email]; ok {
		return &mb, nil
	}
	return nil, repository.ErrNotFound
}

func (r *odMailboxes) Create(_ context.Context, mb *models.Mailbox) error {
	c := *mb
	c.EmailCached = mb.LocalPart + "@" + r.domains.rows[mb.DomainID].Name
	r.byEmail[c.EmailCached] = c
	return nil
}

func odPtr[T any](v T) *T { return &v }

// odOwn is the account's domain before the restore.
func odOwn(id string) models.Domain {
	return models.Domain{
		ID: id, UserID: "u1", Name: "own.org", DocRoot: "/home/alice/domains/own.org/public_html",
		IsEnabled: true, IndexPriority: "html_first", SSLEnabled: true, SSLMode: "letsencrypt",
		NginxCustomDirectives: odPtr("add_header X-Own 1;"), DkimSelector: odPtr("own2026"), DNSSECEnabled: true,
		PHPMemoryLimit: odPtr("256M"), PHPMaxInputVars: odPtr(3000), PHPTimezone: odPtr("Europe/Berlin"),
		PHPPoolID: odPtr("p-own"), EmailEnabled: true, DisclaimerText: odPtr("old footer"),
	}
}

// odMeta is the account's backup: the same domain under backupID, with
// different settings everywhere.
func odMeta(backupID string) *internalbackup.AccountMetadata {
	uname := "alice"
	return &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &uname},
		PHPPools: []internalbackup.MetadataPHPPool{
			{ID: "p-bk", PHPVersion: "8.3", PmMode: "ondemand", PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 10},
		},
		Domains: []internalbackup.MetadataDomain{{
			ID: backupID, Name: "own.org", DocRoot: "/home/alice/domains/elsewhere/public_html",
			IsEnabled: false, RedirectAllTo: odPtr("https://example.net/"), RedirectAllType: odPtr("301"),
			IndexPriority: "php_first", SSLEnabled: false, NginxCustomDirectives: odPtr("return 403;"),
			DkimSelector: odPtr("bk"), DNSSECEnabled: false,
			PHPMemoryLimit: odPtr("768M"), PHPUploadMaxFilesize: odPtr("64M"),
			PHPPoolID: odPtr("p-bk"), RateLimitRPS: 20, ConnectionLimit: 10,
			EmailEnabled: true, CatchallTarget: odPtr("Info@Own.org"),
			DisclaimerEnabled: true, DisclaimerText: odPtr("new footer"),
		}},
	}
}

type odFixture struct {
	domains   *odDomains
	mailboxes *odMailboxes
	scheduled []string
	checked   []models.Domain
	deps      Deps
}

// odSetup is an account with own.org (under ownID), its mailbox
// info@own.org, a PHP 8.4 and an 8.3 pool, on a package whose PHP policy is
// policy. Another account has other.org and its mailbox boss@other.org.
func odSetup(ownID string, overwrite bool, policy string) *odFixture {
	f := &odFixture{}
	f.domains = &odDomains{rows: map[string]models.Domain{
		ownID:   odOwn(ownID),
		"d-oth": {ID: "d-oth", UserID: "u2", Name: "other.org", EmailEnabled: true},
	}}
	f.mailboxes = &odMailboxes{domains: f.domains, byEmail: map[string]models.Mailbox{
		"info@own.org":   {ID: "mb-info", DomainID: ownID, LocalPart: "info", EmailCached: "info@own.org"},
		"boss@other.org": {ID: "mb-boss", DomainID: "d-oth", LocalPart: "boss", EmailCached: "boss@other.org"},
	}}
	pools := &owpPools{ppPools: &ppPools{rows: []models.PHPPool{
		{ID: "p-own", UserID: "u1", PHPVersion: "8.4", PmMode: "ondemand", PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 10},
		{ID: "p-83", UserID: "u1", PHPVersion: "8.3", PmMode: "ondemand", PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 10},
	}}}
	pkg := "pkg1"
	f.deps = Deps{
		Users: &pcUsers{pkg: &pkg}, Packages: &plpPackages{policy: policy},
		Domains: f.domains, Mailboxes: f.mailboxes, PHPPools: pools,
		MailAddresses: &dcReleaser{},
		Untrusted:     true, OverwriteRows: overwrite, KeepExisting: !overwrite,
		ScheduleDomain: func(id string) { f.scheduled = append(f.scheduled, id) },
	}
	// The checks the admin restore door runs, in short: a redirect target
	// must be https, an index priority one the page offers, a PHP size a
	// size; custom nginx directives are refused.
	f.deps.CheckDomain = func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		f.checked = append(f.checked, *row)
		var w []string
		if row.NginxCustomDirectives != nil {
			row.NginxCustomDirectives = nil
			w = append(w, "custom nginx directives dropped: not allowed")
		}
		if row.RedirectAllTo != nil && !strings.HasPrefix(*row.RedirectAllTo, "https://") {
			row.RedirectAllTo, row.RedirectAllType = nil, nil
			w = append(w, "redirect-all dropped: not https")
		}
		if p := row.IndexPriority; p != "" && p != "html_first" && p != "php_first" {
			row.IndexPriority = ""
			w = append(w, "index priority dropped: not one the domain page offers")
		}
		if row.PHPMemoryLimit != nil && !strings.HasSuffix(*row.PHPMemoryLimit, "M") {
			row.PHPMemoryLimit = nil
			w = append(w, "PHP memory_limit dropped: not a size")
		}
		return w, nil
	}
	return f
}

func (f *odFixture) own(id string) models.Domain { return f.domains.rows[id] }

func odStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// The account's domain takes the backup's settings, whether it has the
// backup's id or only its name.
func TestApply_OverwriteGivesTheAccountsDomainTheBackupsSettings(t *testing.T) {
	for name, ids := range map[string][2]string{"same id": {"d-own", "d-own"}, "same name": {"d-own", "d-bk"}} {
		t.Run(name, func(t *testing.T) {
			f := odSetup(ids[0], true, "")
			r := Apply(context.Background(), odMeta(ids[1]), f.deps)

			if len(r.Errors) != 0 {
				t.Fatalf("errors %v", r.Errors)
			}
			got := f.own(ids[0])
			if got.IsEnabled || odStr(got.RedirectAllTo) != "https://example.net/" || odStr(got.RedirectAllType) != "301" || got.IndexPriority != "php_first" {
				t.Errorf("site settings enabled=%v redirect=%s/%s index=%s, want the backup's false, https://example.net/ 301, php_first",
					got.IsEnabled, odStr(got.RedirectAllTo), odStr(got.RedirectAllType), got.IndexPriority)
			}
			if odStr(got.PHPMemoryLimit) != "768M" || odStr(got.PHPUploadMaxFilesize) != "64M" || got.PHPMaxInputVars != nil {
				t.Errorf("PHP limits memory=%s upload=%s input vars=%v, want the backup's 768M, 64M and none", odStr(got.PHPMemoryLimit), odStr(got.PHPUploadMaxFilesize), got.PHPMaxInputVars)
			}
			if odStr(got.PHPTimezone) != "Europe/Berlin" {
				t.Errorf("PHP timezone %s, want Europe/Berlin kept: the backup's limits are the only PHP settings it takes", odStr(got.PHPTimezone))
			}
			if got.RateLimitRPS != 20 || got.ConnectionLimit != 10 {
				t.Errorf("rate limits %d/%d, want 20/10", got.RateLimitRPS, got.ConnectionLimit)
			}
			if odStr(got.PHPPoolID) != "p-83" {
				t.Errorf("PHP pool %s, want p-83, the account's pool for the backup's PHP 8.3 pool", odStr(got.PHPPoolID))
			}
			if odStr(got.CatchallTarget) != "info@own.org" {
				t.Errorf("catch-all %s, want info@own.org", odStr(got.CatchallTarget))
			}
			if !got.DisclaimerEnabled || odStr(got.DisclaimerText) != "new footer" {
				t.Errorf("disclaimer %v %q, want enabled with the backup's text", got.DisclaimerEnabled, odStr(got.DisclaimerText))
			}
			// What the backup never changes on a domain the account has.
			want := odOwn(ids[0])
			if got.ID != want.ID || got.UserID != "u1" || got.Name != want.Name || got.DocRoot != want.DocRoot ||
				odStr(got.NginxCustomDirectives) != odStr(want.NginxCustomDirectives) || !got.SSLEnabled || got.SSLMode != want.SSLMode ||
				odStr(got.DkimSelector) != odStr(want.DkimSelector) || !got.DNSSECEnabled || !got.EmailEnabled {
				t.Errorf("domain %+v, want its id, owner, name, document root, nginx directives, SSL, DKIM, DNSSEC and mail kept", got)
			}
			if len(f.scheduled) != 1 || f.scheduled[0] != ids[0] {
				t.Errorf("scheduled %v, want %s once so its web server config follows", f.scheduled, ids[0])
			}
		})
	}
}

// The checks see the backup's settings on the domain as it is here, without
// its custom nginx directives, which the restore doesn't touch.
func TestApply_OverwriteDomainKeepsItsNginxDirectivesWithoutAWarning(t *testing.T) {
	f := odSetup("d-own", true, "")
	r := Apply(context.Background(), odMeta("d-own"), f.deps)
	if hasError(r.Errors, "nginx") || odStr(f.own("d-own").NginxCustomDirectives) != "add_header X-Own 1;" {
		t.Fatalf("errors %v nginx %s, want the domain's directives kept and nothing reported", r.Errors, odStr(f.own("d-own").NginxCustomDirectives))
	}
	if len(f.checked) != 1 || f.checked[0].DocRoot != "/home/alice/domains/own.org/public_html" || f.checked[0].Name != "own.org" {
		t.Fatalf("checked %+v, want the domain's own name and document root", f.checked)
	}
}

func TestApply_KeepExistingLeavesTheAccountsDomainAsItIs(t *testing.T) {
	f := odSetup("d-own", false, "")
	Apply(context.Background(), odMeta("d-own"), f.deps)
	if len(f.domains.writes) != 0 || len(f.scheduled) != 0 {
		t.Fatalf("writes %v scheduled %v, want none", f.domains.writes, f.scheduled)
	}
}

// A backup with the domain's own settings writes nothing.
func TestApply_OverwriteDomainWithItsOwnSettingsWritesNothing(t *testing.T) {
	f := odSetup("d-own", true, "")
	m := odMeta("d-own")
	own := odOwn("d-own")
	dm := &m.Domains[0]
	dm.IsEnabled, dm.RedirectAllTo, dm.RedirectAllType, dm.IndexPriority = true, nil, nil, "html_first"
	dm.PHPMemoryLimit, dm.PHPUploadMaxFilesize, dm.PHPMaxInputVars = odPtr("256M"), nil, odPtr(3000)
	dm.RateLimitRPS, dm.ConnectionLimit, dm.PHPPoolID = 0, 0, nil
	dm.CatchallTarget, dm.DisclaimerEnabled, dm.DisclaimerText = nil, false, own.DisclaimerText
	r := Apply(context.Background(), m, f.deps)
	if len(f.domains.writes) != 0 || len(f.scheduled) != 0 || len(r.Errors) != 0 {
		t.Fatalf("writes %v scheduled %v errors %v, want none", f.domains.writes, f.scheduled, r.Errors)
	}
}

// A setting the checks refuse leaves the domain's own in place; the rest are
// still taken.
func TestApply_OverwriteDomainKeepsWhatTheChecksRefuse(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.domains.rows["d-own"] = func() models.Domain {
		d := odOwn("d-own")
		d.RedirectAllTo, d.RedirectAllType = odPtr("https://own.example/"), odPtr("302")
		return d
	}()
	m := odMeta("d-own")
	dm := &m.Domains[0]
	dm.RedirectAllTo, dm.IndexPriority, dm.PHPMemoryLimit = odPtr("javascript:alert(1)"), "full-scan", odPtr("lots")
	r := Apply(context.Background(), m, f.deps)

	got := f.own("d-own")
	if odStr(got.RedirectAllTo) != "https://own.example/" || odStr(got.RedirectAllType) != "302" || got.IndexPriority != "html_first" || odStr(got.PHPMemoryLimit) != "256M" {
		t.Fatalf("redirect %s/%s index %s memory %s, want the domain's own kept", odStr(got.RedirectAllTo), odStr(got.RedirectAllType), got.IndexPriority, odStr(got.PHPMemoryLimit))
	}
	for _, want := range []string{
		"domain d-own (own.org): redirect-all dropped: not https",
		"domain d-own (own.org): index priority dropped",
		"domain d-own (own.org): PHP memory_limit dropped: not a size",
	} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v, want %q", r.Errors, want)
		}
	}
	if got.IsEnabled || odStr(got.PHPUploadMaxFilesize) != "64M" || got.RateLimitRPS != 20 {
		t.Fatalf("domain %+v, want the settings the checks allow taken", got)
	}
}

// When the domain fails the restore checks, nothing of the backup's is
// written to it.
func TestApply_OverwriteDomainThatFailsTheChecksIsLeftAsItIs(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.deps.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) {
		return nil, errors.New("nested under another owner's domain")
	}
	r := Apply(context.Background(), odMeta("d-own"), f.deps)
	if len(f.domains.writes) != 0 || len(f.scheduled) != 0 {
		t.Fatalf("writes %v scheduled %v, want none", f.domains.writes, f.scheduled)
	}
	if pcErrors(r, "domain d-own (own.org): settings not updated: nested under another owner's domain") != 1 {
		t.Fatalf("errors %v, want one line", r.Errors)
	}
}

// A PHP limit the backup changes is taken only when the account's package
// lets a tenant set it, as on the PHP settings page; clearing one is a change
// too. A limit the backup doesn't change needs no permission.
func TestApply_OverwriteDomainPHPLimitsFollowThePackagePolicy(t *testing.T) {
	f := odSetup("d-own", true, `{"memory_limit":"admin_only","max_input_vars":"admin_only","post_max_size":"admin_only"}`)
	r := Apply(context.Background(), odMeta("d-own"), f.deps)

	got := f.own("d-own")
	if odStr(got.PHPMemoryLimit) != "256M" || got.PHPMaxInputVars == nil || *got.PHPMaxInputVars != 3000 || odStr(got.PHPUploadMaxFilesize) != "64M" {
		t.Fatalf("memory %s input vars %v upload %s, want 256M and 3000 kept, 64M taken", odStr(got.PHPMemoryLimit), got.PHPMaxInputVars, odStr(got.PHPUploadMaxFilesize))
	}
	for _, want := range []string{
		"domain d-own (own.org): PHP memory_limit not updated: the account's package lets only an administrator set it",
		"domain d-own (own.org): PHP max_input_vars not updated: the account's package lets only an administrator set it",
	} {
		if pcErrors(r, want) != 1 {
			t.Errorf("errors %v, want %q", r.Errors, want)
		}
	}
	if hasError(r.Errors, "post_max_size") {
		t.Errorf("errors %v: post_max_size is unchanged and needs no permission", r.Errors)
	}
}

// When the package can't be read, the PHP limits stay as they are; the
// other settings are still taken.
func TestApply_OverwriteDomainKeepsItsPHPLimitsWhenThePackageCantBeRead(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.deps.Packages = &plpPackages{err: errors.New("db down")}
	r := Apply(context.Background(), odMeta("d-own"), f.deps)

	got := f.own("d-own")
	if odStr(got.PHPMemoryLimit) != "256M" || got.PHPUploadMaxFilesize != nil || got.PHPMaxInputVars == nil {
		t.Fatalf("memory %s upload %s input vars %v, want the domain's own limits", odStr(got.PHPMemoryLimit), odStr(got.PHPUploadMaxFilesize), got.PHPMaxInputVars)
	}
	if !hasError(r.Errors, "domain d-own (own.org): PHP memory_limit not updated: the account's package could not be read") {
		t.Fatalf("errors %v, want the kept limits reported", r.Errors)
	}
	if got.IsEnabled || got.RateLimitRPS != 20 {
		t.Fatalf("domain %+v, want the other settings taken", got)
	}
}

// The catch-all goes only to a mailbox the account had on this server before
// the restore: never an outside address, another account's mailbox, or one
// the backup itself creates, whose password came with the file.
func TestApply_OverwriteDomainCatchallGoesOnlyToAMailboxTheAccountHad(t *testing.T) {
	for name, tc := range map[string]struct {
		target  string
		mailbox string // a mailbox the backup brings for own.org
	}{
		"an outside address":          {"me@gmail.com", ""},
		"another account's mailbox":   {"boss@other.org", ""},
		"a mailbox the backup brings": {"sales@own.org", "sales"},
	} {
		t.Run(name, func(t *testing.T) {
			f := odSetup("d-own", true, "")
			m := odMeta("d-own")
			m.Domains[0].CatchallTarget = odPtr(tc.target)
			if tc.mailbox != "" {
				m.Domains[0].Mailboxes = []internalbackup.MetadataMailbox{{ID: "mb-new", LocalPart: tc.mailbox, PasswordHash: "{SHA512-CRYPT}$6$x"}}
			}
			r := Apply(context.Background(), m, f.deps)

			if got := f.own("d-own").CatchallTarget; got != nil {
				t.Fatalf("catch-all %s, want none (the domain had none)", *got)
			}
			if !hasError(r.Errors, "domain d-own (own.org): catch-all to "+tc.target+" not restored: it isn't a mailbox this account had on this server") {
				t.Fatalf("errors %v, want the refused catch-all reported", r.Errors)
			}
		})
	}
}

// A backup without a catch-all or a disclaimer clears the domain's.
func TestApply_OverwriteDomainClearsTheMailPolicyTheBackupDoesntHave(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.domains.rows["d-own"] = func() models.Domain {
		d := odOwn("d-own")
		d.CatchallTarget, d.DisclaimerEnabled = odPtr("info@own.org"), true
		return d
	}()
	m := odMeta("d-own")
	m.Domains[0].CatchallTarget, m.Domains[0].DisclaimerEnabled, m.Domains[0].DisclaimerText = nil, false, nil
	r := Apply(context.Background(), m, f.deps)

	got := f.own("d-own")
	if got.CatchallTarget != nil || got.DisclaimerEnabled || odStr(got.DisclaimerText) != "" {
		t.Fatalf("catch-all %s disclaimer %v %q (errors %v), want both cleared", odStr(got.CatchallTarget), got.DisclaimerEnabled, odStr(got.DisclaimerText), r.Errors)
	}
}

// Mail policy needs mail on the domain as it is here, whatever the backup
// says about it.
func TestApply_OverwriteDomainMailPolicyNeedsMailOnTheDomain(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.domains.rows["d-own"] = func() models.Domain {
		d := odOwn("d-own")
		d.EmailEnabled = false
		return d
	}()
	r := Apply(context.Background(), odMeta("d-own"), f.deps)

	got := f.own("d-own")
	if got.CatchallTarget != nil || got.DisclaimerEnabled || got.EmailEnabled {
		t.Fatalf("domain %+v, want mail policy and mail left off", got)
	}
	for _, want := range []string{
		"domain d-own (own.org): catch-all not updated: mail is not enabled on the domain",
		"domain d-own (own.org): disclaimer not updated: mail is not enabled on the domain",
	} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v, want %q", r.Errors, want)
		}
	}
}

// A backup pool that wasn't restored leaves the domain on its own pool.
func TestApply_OverwriteDomainKeepsItsPoolWhenTheBackupsWasntRestored(t *testing.T) {
	f := odSetup("d-own", true, "")
	m := odMeta("d-own")
	m.Domains[0].PHPPoolID = odPtr("p-gone")
	r := Apply(context.Background(), m, f.deps)
	if odStr(f.own("d-own").PHPPoolID) != "p-own" {
		t.Fatalf("pool %s, want p-own kept", odStr(f.own("d-own").PHPPoolID))
	}
	if !hasError(r.Errors, "domain d-own (own.org): its PHP pool was not restored; it keeps its own") {
		t.Fatalf("errors %v, want the kept pool reported", r.Errors)
	}
}

// Another account's domain of the same name is not touched.
func TestApply_OverwriteDoesNotTouchAnotherAccountsDomain(t *testing.T) {
	f := odSetup("d-own", true, "")
	other := f.domains.rows["d-oth"]
	m := odMeta("d-oth")
	m.Domains[0].Name = "other.org"
	Apply(context.Background(), m, f.deps)
	if got := f.domains.rows["d-oth"]; got.IsEnabled != other.IsEnabled || got.RedirectAllTo != nil || len(f.domains.writes) != 0 {
		t.Fatalf("other account's domain %+v writes %v, want it untouched", got, f.domains.writes)
	}
}

// A door that doesn't wire the checks updates no existing domain.
func TestApply_OverwriteDomainWithoutTheChecksIsLeftAsItIs(t *testing.T) {
	f := odSetup("d-own", true, "")
	f.deps.CheckDomain = nil
	r := Apply(context.Background(), odMeta("d-own"), f.deps)
	if len(f.domains.writes) != 0 || len(f.scheduled) != 0 {
		t.Fatalf("writes %v scheduled %v, want none", f.domains.writes, f.scheduled)
	}
	if pcErrors(r, "domain d-own (own.org): settings not updated: the restore checks are not wired") != 1 {
		t.Fatalf("errors %v, want one line", r.Errors)
	}
}
