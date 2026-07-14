package plesk

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

func TestParseWPToolkitList_ArrayAndWrapper(t *testing.T) {
	// Real Plesk Obsidian 18 shape: no mainDomain; siteUrl carries www.,
	// fullPath is absolute.
	arr := `[{"id":1,"mainDomainId":1,"path":"\/httpdocs","siteUrl":"https:\/\/www.example.com","version":"6.7.5","fullPath":"\/var\/www\/vhosts\/example.com\/httpdocs"}]`
	rows, err := parseWPToolkitList(arr)
	if err != nil || len(rows) != 1 {
		t.Fatalf("array parse: %v rows=%+v", err, rows)
	}
	if rows[0].SiteURL != "https://www.example.com" || rows[0].Version != "6.7.5" {
		t.Errorf("row = %+v", rows[0])
	}
	if rows[0].FullPath != "/var/www/vhosts/example.com/httpdocs" {
		t.Errorf("fullPath = %q", rows[0].FullPath)
	}

	wrapped := `{"instances":[{"domainName":"shop.example.net","mainDomainPath":"/httpdocs/blog","wpVersion":"6.4"}]}`
	rows2, err := parseWPToolkitList(wrapped)
	if err != nil || len(rows2) != 1 {
		t.Fatalf("wrapper parse: %v rows=%+v", err, rows2)
	}
	if rows2[0].MainDomain != "shop.example.net" || rows2[0].Path != "/httpdocs/blog" || rows2[0].Version != "6.4" {
		t.Errorf("wrapped row = %+v", rows2[0])
	}
}

func TestWPInstance_DomainHostFromSiteURL(t *testing.T) {
	w := wpInstance{SiteURL: "https://blog.example.com/wp"}
	if got := w.domainHost(); got != "blog.example.com" {
		t.Errorf("host from URL = %q, want blog.example.com", got)
	}
	if got := (wpInstance{MainDomain: "a.com", SiteURL: "https://b.com"}).domainHost(); got != "a.com" {
		t.Errorf("main domain must win: %q", got)
	}
}

func TestJoinDocrootPath(t *testing.T) {
	dom := migrate.DomainSpec{Name: "example.com", DocRoot: "/var/www/vhosts/example.com/httpdocs"}
	cases := map[string]string{
		"/httpdocs":            "/var/www/vhosts/example.com/httpdocs",
		"/httpdocs/wordpress":  "/var/www/vhosts/example.com/httpdocs/wordpress",
		"":                     "/var/www/vhosts/example.com/httpdocs",
		"/var/www/vhosts/x/wp": "/var/www/vhosts/x/wp", // already absolute
	}
	for in, want := range cases {
		if got := joinDocrootPath(dom, in); got != want {
			t.Errorf("joinDocrootPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribeWordPress_FiltersToAccountDomains(t *testing.T) {
	d := New()
	// Server has two instances; only example.com belongs to this account.
	s := fixtureSession(map[string]string{
		// siteUrl carries www.; matching must normalise. fullPath is used
		// for Path. Second instance belongs to another account → filtered.
		"wp-toolkit --list -format json": `[
			{"siteUrl":"https://www.example.com","version":"6.7.5","fullPath":"/var/www/vhosts/example.com/httpdocs"},
			{"siteUrl":"https://other.tld","version":"6.5","fullPath":"/var/www/vhosts/other.tld/httpdocs"}
		]`,
	})
	domains := []migrate.DomainSpec{{Name: "example.com", DocRoot: "/var/www/vhosts/example.com/httpdocs"}}
	apps, warns := d.describeWordPress(context.Background(), s, domains)
	if len(apps) != 1 {
		t.Fatalf("got %d apps, want 1 (filtered to account domains): %+v", len(apps), apps)
	}
	if apps[0].Kind != "wordpress" || apps[0].Path != "/var/www/vhosts/example.com/httpdocs" || apps[0].Version != "6.7.5" {
		t.Errorf("app = %+v", apps[0])
	}
	if len(warns) != 0 {
		t.Errorf("clean run should have no warnings: %+v", warns)
	}
}

func TestDescribeWordPress_FallbackScanOnToolkitError(t *testing.T) {
	d := New()
	// wp-toolkit returns non-JSON (extension absent) → parse fails →
	// docroot scan runs; the test-and-echo reports wp-config present.
	fs := fixtureSession(map[string]string{
		"wp-toolkit --list": "ERROR: extension not installed",
		"wp-config.php":     "found\n",
	})
	domains := []migrate.DomainSpec{{Name: "example.com", DocRoot: "/var/www/vhosts/example.com/httpdocs"}}
	apps, warns := d.describeWordPress(context.Background(), fs, domains)
	if len(apps) != 1 || apps[0].Path != "/var/www/vhosts/example.com/httpdocs" {
		t.Fatalf("fallback scan should find wp-config: %+v", apps)
	}
	if len(warns) == 0 {
		t.Error("fallback must record a warning")
	}
}
