package commands

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH #2422: a site with no index file gets Jabali's default page from outside
// the docroot, through the last entry of the index directive.

func TestIndexDirectiveFor_EndsWithDefaultPage(t *testing.T) {
	for _, priority := range []string{"", "php_first", "html_only", "php_only", "full", "bogus"} {
		got := indexDirectiveFor(priority)
		if !strings.HasPrefix(got, "index ") || !strings.HasSuffix(got, " "+defaultPageURI+";") {
			t.Errorf("indexDirectiveFor(%q) = %q, want index ... %s;", priority, got, defaultPageURI)
		}
		// The site's own index files come first, so a site that has one never
		// reaches the default page.
		files := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(got, "index "), ";"))
		if len(files) < 2 || files[len(files)-1] != defaultPageURI {
			t.Errorf("indexDirectiveFor(%q) = %q: the default page must be the last of several entries", priority, got)
		}
	}
}

func defaultPageVhostData(page string) vhostData {
	vd := gh896VhostData(true)
	vd.IndexDirective = indexDirectiveFor("")
	vd.DefaultPage = page
	return vd
}

func TestVhost_DefaultPageLocation(t *testing.T) {
	page := "/var/www/jabali-default-pages/example.com.html"
	out := mustRenderVhost(t, defaultPageVhostData(page))

	servers := strings.Count(out, "    "+indexDirectiveFor(""))
	if servers == 0 {
		t.Fatalf("index directive missing:\n%s", out)
	}
	if n := strings.Count(out, "location = /jabali-default-page.html {"); n != servers {
		t.Fatalf("default page location rendered %d times, want %d (one per server block that serves the site):\n%s", n, servers, out)
	}

	start := strings.Index(out, "location = /jabali-default-page.html {")
	end := strings.Index(out[start:], "\n    }")
	block := out[start : start+end]
	for _, want := range []string{
		"internal;",
		`if ($request_uri !~ "^/(\?.*)?$") { return 403; }`,
		"alias " + page + ";",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("default page location lacks %q:\n%s", want, block)
		}
	}
	// An add_header here would drop every header the server block sets.
	if strings.Contains(block, "add_header") {
		t.Errorf("default page location must not set headers:\n%s", block)
	}
}

func TestVhost_NoDefaultPageLocationWhenUnset(t *testing.T) {
	out := mustRenderVhost(t, defaultPageVhostData(""))
	if strings.Contains(out, "location = /jabali-default-page.html") {
		t.Errorf("default page location rendered with no page:\n%s", out)
	}
}

// withDefaultPagesDir points the default page at a temp dir owned by the
// test user's group, with the real getent for the group lookup.
func withDefaultPagesDir(t *testing.T) string {
	t.Helper()
	withRealExec(t)
	u, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("group of current user: %v", err)
	}
	prevDir, prevGroup := defaultPagesDir, defaultPageGroup
	defaultPagesDir = filepath.Join(t.TempDir(), "pages")
	defaultPageGroup = g.Name
	t.Cleanup(func() {
		defaultPagesDir, defaultPageGroup = prevDir, prevGroup
	})
	return defaultPagesDir
}

func TestWriteDefaultPage_BuiltinNamesOnlyTheDomain(t *testing.T) {
	dir := withDefaultPagesDir(t)
	docRoot := "/home/alice/domains/example.com/public_html"
	if err := writeDefaultPage("alice", "example.com", docRoot, ""); err != nil {
		t.Fatalf("writeDefaultPage: %v", err)
	}

	path := filepath.Join(dir, "example.com.html")
	if defaultPagePath("example.com") != path {
		t.Fatalf("defaultPagePath = %q, want %q", defaultPagePath("example.com"), path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	if !strings.Contains(string(body), "<h1>example.com</h1>") {
		t.Errorf("page does not name the domain:\n%s", body)
	}
	// The page is public: no login name, no server path.
	for _, leak := range []string{"alice", docRoot, "/home/"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("built-in page shows %q:\n%s", leak, body)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("page mode = %o, want 640", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o750 {
		t.Errorf("dir mode = %o, want 750", di.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".page.*.tmp")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestWriteDefaultPage_CustomTemplate(t *testing.T) {
	dir := withDefaultPagesDir(t)
	tmpl := "<p>{{.Domain}} for {{.Username}} at {{.DocRoot}}</p>"
	if err := writeDefaultPage("alice", "example.com", "/srv/x", tmpl); err != nil {
		t.Fatalf("writeDefaultPage: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "example.com.html"))
	if got, want := string(body), "<p>example.com for alice at /srv/x</p>"; got != want {
		t.Errorf("page = %q, want %q", got, want)
	}
}

func TestWriteDefaultPage_MalformedTemplateFallsBack(t *testing.T) {
	dir := withDefaultPagesDir(t)
	if err := writeDefaultPage("alice", "example.com", "/srv/x", "<p>{{.Domain</p>"); err != nil {
		t.Fatalf("writeDefaultPage: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "example.com.html"))
	if !strings.Contains(string(body), "<h1>example.com</h1>") {
		t.Errorf("malformed template did not fall back to the built-in page:\n%s", body)
	}
}

func TestWriteDefaultPage_UnchangedIsNotRewritten(t *testing.T) {
	dir := withDefaultPagesDir(t)
	if err := writeDefaultPage("alice", "example.com", "/srv/x", ""); err != nil {
		t.Fatalf("first write: %v", err)
	}
	path := filepath.Join(dir, "example.com.html")
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	if err := writeDefaultPage("alice", "example.com", "/srv/x", ""); err != nil {
		t.Fatalf("second write: %v", err)
	}
	fi, _ := os.Stat(path)
	if !fi.ModTime().Equal(past) {
		t.Errorf("unchanged page was rewritten (mtime %v, want %v)", fi.ModTime(), past)
	}

	// A changed template does rewrite it.
	if err := writeDefaultPage("alice", "example.com", "/srv/x", "<p>{{.Domain}}</p>"); err != nil {
		t.Fatalf("third write: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "<p>example.com</p>" {
		t.Errorf("changed template not written: %q", body)
	}
}
