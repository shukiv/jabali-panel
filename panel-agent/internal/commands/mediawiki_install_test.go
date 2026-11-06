package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestComputeMediaWikiInstallPath(t *testing.T) {
	cases := []struct{ docroot, subdir, want string }{
		{"/home/alice/domains/x/public_html", "", "/home/alice/domains/x/public_html"},
		{"/home/alice/domains/x/public_html", "wiki", "/home/alice/domains/x/public_html/wiki"},
	}
	for _, c := range cases {
		if got, err := computeMediaWikiInstallPath(c.docroot, c.subdir); err != nil || got != c.want {
			t.Errorf("computeMediaWikiInstallPath(%q, %q) = %q, %v, want %q", c.docroot, c.subdir, got, err, c.want)
		}
	}
}

func TestComputeMediaWikiScriptPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"wiki", "/wiki"},
		{"wiki/", "/wiki"},
	}
	for _, c := range cases {
		if got := computeMediaWikiScriptPath(c.in); got != c.want {
			t.Errorf("computeMediaWikiScriptPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComputeMediaWikiServerURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com/wiki", "https://example.com"},
		{"https://example.com", "https://example.com"},
		{"https://example.com:8443/foo/bar", "https://example.com:8443"},
		// Garbage URL → returned unchanged so the CLI installer's own
		// error reporting can surface the real problem.
		{"not-a-url", "not-a-url"},
	}
	for _, c := range cases {
		if got := computeMediaWikiServerURL(c.in); got != c.want {
			t.Errorf("computeMediaWikiServerURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMediaWikiMinorSeries(t *testing.T) {
	if got := mediawikiMinorSeries(); !strings.HasPrefix(got, "1.") {
		t.Fatalf("mediawikiMinorSeries() = %q (expected to start with 1.)", got)
	}
	parts := strings.Split(mediawikiMinorSeries(), ".")
	if len(parts) != 2 {
		t.Fatalf("mediawikiMinorSeries() should be major.minor, got %q", mediawikiMinorSeries())
	}
}

func TestMediaWikiTarballURL_HasVersion(t *testing.T) {
	if !strings.Contains(mediawikiTarballURL, mediawikiVersion) {
		t.Errorf("URL %q should reference version %q", mediawikiTarballURL, mediawikiVersion)
	}
	if !strings.HasPrefix(mediawikiTarballURL, "https://releases.wikimedia.org/mediawiki/") {
		t.Errorf("URL %q should originate from releases.wikimedia.org", mediawikiTarballURL)
	}
}

func TestMediaWikiAdminUserPattern(t *testing.T) {
	good := []string{"Admin", "Wiki Sysop", "Admin_Two", "Bob123"}
	bad := []string{"", "admin" /* lowercase */, "1Admin" /* digit start */, "Admin!" /* punct */}
	for _, g := range good {
		if !mediawikiAdminUserPattern.MatchString(g) {
			t.Errorf("expected %q to match", g)
		}
	}
	for _, b := range bad {
		if mediawikiAdminUserPattern.MatchString(b) {
			t.Errorf("expected %q NOT to match", b)
		}
	}
}

func TestMediaWikiLanguagePattern(t *testing.T) {
	good := []string{"en", "he", "fr", "en-gb", "zh-hant"}
	bad := []string{"", "EN", "english", "x", "en_US"}
	for _, g := range good {
		if !mediawikiLanguagePattern.MatchString(g) {
			t.Errorf("expected %q to match", g)
		}
	}
	for _, b := range bad {
		if mediawikiLanguagePattern.MatchString(b) {
			t.Errorf("expected %q NOT to match", b)
		}
	}
}

func TestMediawikiInstallHandler_RejectsBadJSON(t *testing.T) {
	_, err := mediawikiInstallHandler(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *agentwire.AgentError, got %T", err)
	}
	if ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("code = %q", ae.Code)
	}
}

func TestMediawikiInstallHandler_ValidationTable(t *testing.T) {
	good := `{"os_user":"alice","docroot":"/home/alice/domains/y/public_html","db_name":"d","db_user":"u","db_password":"p","site_title":"t","admin_user":"Admin","admin_pass":"longpassword","admin_email":"a@b.c","language":"en"}`
	cases := []struct {
		name string
		body string
	}{
		// Sanity: the "good" body should NOT be in this table — the
		// validations below mutate one field at a time.
		{"missing os_user", strings.Replace(good, `"os_user":"alice",`, "", 1)},
		{"missing docroot", strings.Replace(good, `"docroot":"/home/alice/domains/y/public_html",`, "", 1)},
		{"missing db", strings.Replace(good, `"db_name":"d","db_user":"u","db_password":"p",`, "", 1)},
		{"missing site_title", strings.Replace(good, `"site_title":"t",`, "", 1)},
		{"missing admin_user", strings.Replace(good, `"admin_user":"Admin",`, "", 1)},
		{"lowercase admin_user", strings.Replace(good, `"admin_user":"Admin"`, `"admin_user":"admin"`, 1)},
		{"short admin_pass", strings.Replace(good, `"admin_pass":"longpassword"`, `"admin_pass":"short"`, 1)},
		{"missing admin_email", strings.Replace(good, `"admin_email":"a@b.c",`, "", 1)},
		{"bad language", strings.Replace(good, `"language":"en"`, `"language":"english"`, 1)},
		{"docroot escape", strings.Replace(good, `"docroot":"/home/alice/domains/y/public_html"`, `"docroot":"/etc"`, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := mediawikiInstallHandler(context.Background(), json.RawMessage(c.body))
			if err == nil {
				t.Fatalf("expected error for %q, got nil", c.name)
			}
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) {
				t.Fatalf("want *agentwire.AgentError, got %T", err)
			}
			if ae.Code != agentwire.CodeInvalidArgument {
				t.Errorf("code = %q (want %q)", ae.Code, agentwire.CodeInvalidArgument)
			}
		})
	}
}

func TestMediaWikiAppDescriptor_RegisteredOnInstaller(t *testing.T) {
	appDispatchMu.RLock()
	_, ok := appInstallers["mediawiki"]
	appDispatchMu.RUnlock()
	if !ok {
		t.Fatal("mediawiki installer not registered on appInstallers")
	}
	appDispatchMu.RLock()
	_, ok = appDeleters["mediawiki"]
	appDispatchMu.RUnlock()
	if !ok {
		t.Fatal("mediawiki deleter not registered on appDeleters")
	}
}
