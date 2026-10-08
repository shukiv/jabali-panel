package backupmetadata

import (
	"context"
	"sort"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a backup carries every per-domain PHP setting the PHP settings
// page writes, not only the six limits, and a restore brings them back under
// the same rules: the page's checks, and from an uploaded file the package's
// PHP policy.

// psSettings sets every PHP setting beyond the limits on dm, with open_basedir
// naming a folder in /home/<user>, and marks the backup as carrying them.
func psSettings(dm *internalbackup.MetadataDomain, user string) {
	dm.PHPDisplayErrors, dm.PHPLogErrors, dm.PHPFileUploads = odPtr(true), odPtr(false), odPtr(false)
	dm.PHPShortOpenTag, dm.PHPAllowURLFopen = odPtr(true), odPtr(true)
	dm.PHPErrorReporting, dm.PHPTimezone = odPtr(0), odPtr("Asia/Jerusalem")
	dm.PHPOpenBasedir = odPtr("{WEBSPACEROOT}:/home/" + user + "/lib:{TMP}")
	dm.PHPSettingsComplete = true
}

// psSettingsOf is the PHP settings beyond the limits on d, as text.
func psSettingsOf(d models.Domain) string {
	b := func(p *bool) string {
		if p == nil {
			return "<nil>"
		}
		if *p {
			return "on"
		}
		return "off"
	}
	er := "<nil>"
	if d.PHPErrorReporting != nil {
		er = intKey(d.PHPErrorReporting)[1:]
	}
	return strings.Join([]string{"display_errors=" + b(d.PHPDisplayErrors), "error_reporting=" + er,
		"date.timezone=" + odStr(d.PHPTimezone), "log_errors=" + b(d.PHPLogErrors), "file_uploads=" + b(d.PHPFileUploads),
		"short_open_tag=" + b(d.PHPShortOpenTag), "open_basedir=" + odStr(d.PHPOpenBasedir), "allow_url_fopen=" + b(d.PHPAllowURLFopen)}, " ")
}

const psWant = "display_errors=on error_reporting=0 date.timezone=Asia/Jerusalem log_errors=off file_uploads=off " +
	"short_open_tag=on open_basedir={WEBSPACEROOT}:/home/alice/lib:{TMP} allow_url_fopen=on"

func TestBuild_CarriesDomainPHPSettings(t *testing.T) {
	dom := models.Domain{ID: "d1", Name: "shop.org", DocRoot: "/home/alice/domains/shop.org/public_html"}
	dom.PHPDisplayErrors, dom.PHPLogErrors, dom.PHPFileUploads = odPtr(true), odPtr(false), odPtr(false)
	dom.PHPShortOpenTag, dom.PHPAllowURLFopen = odPtr(true), odPtr(true)
	dom.PHPErrorReporting, dom.PHPTimezone = odPtr(0), odPtr("Asia/Jerusalem")
	dom.PHPOpenBasedir = odPtr("{WEBSPACEROOT}:/home/alice/lib:{TMP}")
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{Domains: &fDomains{rows: []models.Domain{dom, {ID: "d2", Name: "bare.org"}}}})
	if len(m.Domains) != 2 {
		t.Fatalf("backup has %d domains, want 2", len(m.Domains))
	}
	var back models.Domain
	setBackupPHPSettings(&back, m.Domains[0], "alice", "alice")
	if got := psSettingsOf(back); got != psWant {
		t.Errorf("round trip:\n got  %s\n want %s", got, psWant)
	}
	// A domain that sets none of them still says the backup carries them, so
	// a restore over an existing domain clears its own.
	for _, dm := range m.Domains {
		if !dm.PHPSettingsComplete {
			t.Errorf("domain %s: the backup doesn't say it carries the PHP settings", dm.Name)
		}
	}
}

// A new domain takes the backup's PHP settings.
func TestApply_RestoredDomainTakesItsPHPSettings(t *testing.T) {
	m := plpMeta(false)
	psSettings(&m.Domains[0], "alice")
	dom, r := plpApply(m, true, nil, &plpPackages{})
	if len(dom.created) != 1 || hasError(r.Errors, "PHP ") {
		t.Fatalf("created %d errors %v", len(dom.created), r.Errors)
	}
	if got := psSettingsOf(dom.created[0]); got != psWant {
		t.Fatalf("restored:\n got  %s\n want %s", got, psWant)
	}
}

// open_basedir names the account's folders under the username the backup was
// made with; they move onto this server's username before the checks, as the
// document root does. A path in another home is left for the checks to refuse.
func TestApply_RestoredOpenBasedirMovesOntoThisServersUsername(t *testing.T) {
	doms := &ppDomains{pools: &ppPools{}}
	var checked []string
	meta := rhMeta("bob", "/home/bob/domains/alice.org/public_html")
	meta.Domains[0].PHPOpenBasedir = odPtr("{WEBSPACEROOT}:/home/bob/lib:/home/bobby/x:{TMP}")
	meta.Domains[0].PHPSettingsComplete = true
	check := func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		checked = append(checked, odStr(row.PHPOpenBasedir))
		return nil, nil
	}
	Apply(context.Background(), meta, Deps{Users: namedUsersRepo{username: "alice"}, Domains: doms, CheckDomain: check})
	want := "{WEBSPACEROOT}:/home/alice/lib:/home/bobby/x:{TMP}"
	if len(checked) != 1 || checked[0] != want {
		t.Fatalf("checks saw open_basedir %v, want [%s]", checked, want)
	}
}

// From an uploaded file, open_basedir and allow_url_fopen are security
// settings: an account without a package, or whose package doesn't let the
// tenant set them (tenant_privileged), doesn't get them. The other settings
// follow the package as the limits do.
func TestApply_UploadedDomainHoldsEveryPHPSettingToItsPackage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pkg     *string
		policy  string
		dropped []string
	}{
		{"no package", nil, "", []string{"open_basedir", "allow_url_fopen"}},
		{"default policy", odPtr("pkg1"), "", []string{"open_basedir", "allow_url_fopen"}},
		{"privileged", odPtr("pkg1"), `{"open_basedir":"tenant_privileged","allow_url_fopen":"tenant_privileged"}`, nil},
		{"standard locked", odPtr("pkg1"),
			`{"display_errors":"admin_only","error_reporting":"admin_only","date.timezone":"admin_only","log_errors":"admin_only",` +
				`"file_uploads":"admin_only","short_open_tag":"admin_only","open_basedir":"tenant_privileged","allow_url_fopen":"tenant_privileged"}`,
			[]string{"display_errors", "error_reporting", "date.timezone", "log_errors", "file_uploads", "short_open_tag"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := plpMeta(false)
			psSettings(&m.Domains[0], "alice")
			dom, r := plpApply(m, false, tc.pkg, &plpPackages{policy: tc.policy})
			if len(dom.created) != 1 {
				t.Fatalf("created %d (errors %v)", len(dom.created), r.Errors)
			}
			got := psSettingsOf(dom.created[0])
			for _, d := range []string{"display_errors", "error_reporting", "date.timezone", "log_errors", "file_uploads", "short_open_tag", "open_basedir", "allow_url_fopen"} {
				dropped := false
				for _, x := range tc.dropped {
					dropped = dropped || x == d
				}
				unset := strings.Contains(" "+got+" ", " "+d+"=<nil> ")
				reported := hasError(r.Errors, "PHP "+d+" not restored: the account's package lets only an administrator set it")
				if unset != dropped || reported != dropped {
					t.Errorf("%s: unset %v reported %v, want %v (got %s; errors %v)", d, unset, reported, dropped, got, r.Errors)
				}
			}
		})
	}
}

// The overwrite takes each PHP setting the backup changes, as the PHP settings
// page would for the tenant; a setting the backup doesn't have is cleared.
func TestApply_OverwriteDomainTakesTheBackupsPHPSettings(t *testing.T) {
	f := odSetup("d-own", true, `{"open_basedir":"tenant_privileged","allow_url_fopen":"tenant_privileged"}`)
	own := f.domains.rows["d-own"]
	own.PHPFileUploads, own.PHPAllowURLFopen = odPtr(true), odPtr(false) // the backup turns them off and on
	f.domains.rows["d-own"] = own
	m := odMeta("d-own")
	psSettings(&m.Domains[0], "alice")
	m.Domains[0].PHPTimezone = nil
	r := Apply(context.Background(), m, f.deps)

	want := strings.Replace(psWant, "date.timezone=Asia/Jerusalem", "date.timezone=<nil>", 1)
	if got := psSettingsOf(f.own("d-own")); got != want {
		t.Fatalf("domain:\n got  %s\n want %s\n(errors %v)", got, want, r.Errors)
	}
	if hasError(r.Errors, "PHP ") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// open_basedir moves onto this server's username on the overwrite too.
func TestApply_OverwriteDomainMovesOpenBasedirOntoThisServersUsername(t *testing.T) {
	f := odSetup("d-own", true, `{"open_basedir":"tenant_privileged"}`)
	f.deps.Users = psUsers{username: "carol", pkg: "pkg1"}
	m := odMeta("d-own")
	m.Domains[0].PHPOpenBasedir = odPtr("/home/alice/lib")
	m.Domains[0].PHPSettingsComplete = true
	Apply(context.Background(), m, f.deps)
	if len(f.checked) != 1 || odStr(f.checked[0].PHPOpenBasedir) != "/home/carol/lib" {
		t.Fatalf("checked %d rows, open_basedir %v; want /home/carol/lib", len(f.checked), f.checked)
	}
}

// The checks see only the PHP settings the backup changes. One the domain
// keeps isn't checked again: an administrator's open_basedir there may be a
// path the tenant's rules for an uploaded file would refuse.
func TestApply_OverwriteChecksOnlyThePHPSettingsTheBackupChanges(t *testing.T) {
	f := odSetup("d-own", true, `{"open_basedir":"tenant_privileged"}`)
	own := f.domains.rows["d-own"]
	own.PHPOpenBasedir = odPtr("{WEBSPACEROOT}:/srv/shared")
	f.domains.rows["d-own"] = own
	m := odMeta("d-own")
	m.Domains[0].PHPOpenBasedir, m.Domains[0].PHPDisplayErrors = odPtr("{WEBSPACEROOT}:/srv/shared"), odPtr(true)
	m.Domains[0].PHPSettingsComplete = true
	r := Apply(context.Background(), m, f.deps)
	if len(f.checked) != 1 || f.checked[0].PHPOpenBasedir != nil || f.checked[0].PHPDisplayErrors == nil {
		t.Fatalf("checked %d rows; want the changed display_errors checked and the unchanged open_basedir not (errors %v)", len(f.checked), r.Errors)
	}
	if got := f.own("d-own"); odStr(got.PHPOpenBasedir) != "{WEBSPACEROOT}:/srv/shared" || got.PHPDisplayErrors == nil || !*got.PHPDisplayErrors {
		t.Fatalf("domain:\n %s\nwant open_basedir kept and display_errors on", psSettingsOf(got))
	}
}

// A backup made before it carried these settings leaves the domain's own.
func TestApply_OverwriteFromAnOlderBackupKeepsTheDomainsPHPSettings(t *testing.T) {
	f := odSetup("d-own", true, "")
	own := f.domains.rows["d-own"]
	own.PHPDisplayErrors, own.PHPLogErrors, own.PHPFileUploads = odPtr(true), odPtr(false), odPtr(false)
	own.PHPShortOpenTag, own.PHPAllowURLFopen = odPtr(true), odPtr(true)
	own.PHPErrorReporting, own.PHPTimezone = odPtr(0), odPtr("Asia/Jerusalem")
	own.PHPOpenBasedir = odPtr("{WEBSPACEROOT}:/home/alice/lib:{TMP}")
	f.domains.rows["d-own"] = own
	r := Apply(context.Background(), odMeta("d-own"), f.deps)
	if got := psSettingsOf(f.own("d-own")); got != psWant {
		t.Fatalf("domain:\n got  %s\n want %s\n(errors %v)", got, psWant, r.Errors)
	}
}

// psUsers answers with the account under username, on package pkg.
type psUsers struct {
	repository.UserRepository
	username, pkg string
}

func (r psUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	u, p := r.username, r.pkg
	return &models.User{ID: id, Username: &u, PackageID: &p}, nil
}

// The restore holds every directive the PHP settings page writes to the
// package's policy, on a new domain and on an overwrite.
func TestRestoredPHPSettingTables_CoverEveryPageDirective(t *testing.T) {
	want := models.PHPPolicyDirectives()
	sort.Strings(want)
	var policy, overwrite []string
	for _, f := range phpLimitFields(&models.Domain{}) {
		policy = append(policy, f.directive)
	}
	for _, f := range domainPHPLimits {
		overwrite = append(overwrite, f.directive)
	}
	for name, got := range map[string][]string{"new domain": policy, "overwrite": overwrite} {
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s covers %v, want %v", name, got, want)
		}
	}
}
