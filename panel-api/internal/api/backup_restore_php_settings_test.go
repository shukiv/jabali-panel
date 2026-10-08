package api

import (
	"context"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restored domain brings back every per-domain PHP setting, each
// held to the rule the PHP settings page applies to it.

func phpCheck(source RestoreSource) func(context.Context, *models.Domain, string) ([]string, error) {
	return RestoreDomainCheck(rdcDomains{}, rdcAliases{}, rdcOwnerOptions(true), nil, source)
}

func TestRestoreDomainCheck_KeepsValidPHPSettings(t *testing.T) {
	for _, source := range []RestoreSource{RestoreFromOwnBackup, RestoreFromUpload} {
		row := rdcRow("site.org")
		row.PHPDisplayErrors, row.PHPLogErrors, row.PHPFileUploads = boolPtr(true), boolPtr(false), boolPtr(false)
		row.PHPShortOpenTag, row.PHPAllowURLFopen = boolPtr(true), boolPtr(false)
		row.PHPErrorReporting, row.PHPTimezone = intp(0), strp("Asia/Jerusalem")
		row.PHPOpenBasedir = strp("{WEBSPACEROOT}:/home/alice/lib:{TMP}")
		w, err := phpCheck(source)(context.Background(), row, "alice")
		if err != nil || len(w) != 0 {
			t.Fatalf("source %d: warnings %v err %v", source, w, err)
		}
		if !*row.PHPDisplayErrors || *row.PHPLogErrors || *row.PHPFileUploads || !*row.PHPShortOpenTag || *row.PHPAllowURLFopen ||
			*row.PHPErrorReporting != 0 || *row.PHPTimezone != "Asia/Jerusalem" || *row.PHPOpenBasedir != "{WEBSPACEROOT}:/home/alice/lib:{TMP}" {
			t.Fatalf("source %d: valid settings changed: %+v", source, row)
		}
	}
}

// A setting the PHP settings page would refuse is dropped with a warning; the
// domain and its other settings stay.
func TestRestoreDomainCheck_DropsPHPSettingsTheSettingsPageRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(*models.Domain)
		unset func(*models.Domain) bool
	}{
		{"error_reporting", func(d *models.Domain) { d.PHPErrorReporting = intp(40000) }, func(d *models.Domain) bool { return d.PHPErrorReporting == nil }},
		{"error_reporting", func(d *models.Domain) { d.PHPErrorReporting = intp(-1) }, func(d *models.Domain) bool { return d.PHPErrorReporting == nil }},
		{"date.timezone", func(d *models.Domain) { d.PHPTimezone = strp("Mars/Base") }, func(d *models.Domain) bool { return d.PHPTimezone == nil }},
		{"date.timezone", func(d *models.Domain) { d.PHPTimezone = strp("UTC\nx=1") }, func(d *models.Domain) bool { return d.PHPTimezone == nil }},
		{"open_basedir", func(d *models.Domain) { d.PHPOpenBasedir = strp("/") }, func(d *models.Domain) bool { return d.PHPOpenBasedir == nil }},
		{"open_basedir", func(d *models.Domain) { d.PHPOpenBasedir = strp("{WEBSPACEROOT}:/home/bob") }, func(d *models.Domain) bool { return d.PHPOpenBasedir == nil }},
		{"open_basedir", func(d *models.Domain) { d.PHPOpenBasedir = strp("/home") }, func(d *models.Domain) bool { return d.PHPOpenBasedir == nil }},
		{"open_basedir", func(d *models.Domain) { d.PHPOpenBasedir = strp("{WEBSPACEROOT}::{TMP}") }, func(d *models.Domain) bool { return d.PHPOpenBasedir == nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rdcRow("site.org")
			row.PHPDisplayErrors = boolPtr(true)
			tc.set(row)
			w, err := phpCheck(RestoreFromOwnBackup)(context.Background(), row, "alice")
			if err != nil || !tc.unset(row) || len(w) != 1 || !strings.Contains(w[0], "PHP "+tc.name+" dropped") {
				t.Fatalf("got warnings %v err %v; want it dropped with one warning", w, err)
			}
			if row.PHPDisplayErrors == nil || !*row.PHPDisplayErrors {
				t.Fatalf("a valid setting beside it was dropped: %v", row.PHPDisplayErrors)
			}
		})
	}
}

// open_basedir is held to the admin rules from this server's own backup and to
// the tenant's from an uploaded file: there an entry outside the owner's home
// is dropped, as the PHP settings page refuses it for the tenant.
func TestRestoreDomainCheck_OpenBasedirFollowsTheSource(t *testing.T) {
	row := rdcRow("site.org")
	row.PHPOpenBasedir = strp("{WEBSPACEROOT}:/srv/shared")
	w, err := phpCheck(RestoreFromOwnBackup)(context.Background(), row, "alice")
	if err != nil || len(w) != 0 || row.PHPOpenBasedir == nil || *row.PHPOpenBasedir != "{WEBSPACEROOT}:/srv/shared" {
		t.Fatalf("own backup: warnings %v err %v open_basedir %v; want the admin path kept", w, err, row.PHPOpenBasedir)
	}

	row = rdcRow("site.org")
	row.PHPOpenBasedir = strp("{WEBSPACEROOT}:/srv/shared")
	w, err = phpCheck(RestoreFromUpload)(context.Background(), row, "alice")
	if err != nil || row.PHPOpenBasedir != nil || len(w) != 1 || !strings.Contains(w[0], "PHP open_basedir dropped") || !strings.Contains(w[0], "outside your home directory") {
		t.Fatalf("upload: warnings %v err %v open_basedir %v; want it dropped as outside the home", w, err, row.PHPOpenBasedir)
	}
}

// The stored open_basedir is the page's canonical form: duplicates dropped.
func TestRestoreDomainCheck_NormalizesOpenBasedir(t *testing.T) {
	row := rdcRow("site.org")
	row.PHPOpenBasedir = strp(" {WEBSPACEROOT}:{TMP}:{WEBSPACEROOT} ")
	w, err := phpCheck(RestoreFromUpload)(context.Background(), row, "alice")
	if err != nil || len(w) != 0 || row.PHPOpenBasedir == nil || *row.PHPOpenBasedir != "{WEBSPACEROOT}:{TMP}" {
		t.Fatalf("warnings %v err %v open_basedir %v; want {WEBSPACEROOT}:{TMP}", w, err, row.PHPOpenBasedir)
	}
}

// Every directive the PHP settings page writes has a restore rule, so a new
// one can't come back from a backup unchecked.
func TestRestoredPHPSettingRules_CoverEveryPageDirective(t *testing.T) {
	var got []string
	for _, r := range restoredPHPSettingRules {
		got = append(got, r.directive)
	}
	want := models.PHPPolicyDirectives()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("restore rules cover %v, want %v", got, want)
	}
}
