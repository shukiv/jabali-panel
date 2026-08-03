package commands

import (
	"strings"
	"testing"
)

// GH #631: the paste store must land OUTSIDE the docroot (nginx must never
// be able to serve it), and subdir installs must not collide.
func TestPrivatebinDataDir(t *testing.T) {
	cases := []struct{ docroot, subdir, want string }{
		{"/home/u/domains/d.example/public_html", "", "/home/u/domains/d.example/privatebin-data"},
		{"/home/u/domains/d.example/public_html", "paste", "/home/u/domains/d.example/privatebin-data-paste"},
		{"/home/u/domains/d.example/public_html", "tools/paste", "/home/u/domains/d.example/privatebin-data-tools-paste"},
	}
	for _, c := range cases {
		got := privatebinDataDir(c.docroot, c.subdir)
		if got != c.want {
			t.Errorf("privatebinDataDir(%q,%q) = %q, want %q", c.docroot, c.subdir, got, c.want)
		}
		if strings.HasPrefix(got, c.docroot+"/") {
			t.Errorf("data dir %q is INSIDE the docroot — pastes would be nginx-fetchable", got)
		}
	}
}

// INI values must not be able to break out of their quotes or smuggle a
// second line into cfg/conf.php.
func TestPrivatebinINIQuoted(t *testing.T) {
	if got := privatebinINIQuoted(`My "Bin"` + "\ninjected = true"); strings.ContainsAny(got[1:len(got)-1], "\"\n\r") {
		t.Errorf("quotes/newlines survived: %q", got)
	}
	if got := privatebinINIQuoted("PrivateBin"); got != `"PrivateBin"` {
		t.Errorf("plain title mangled: %q", got)
	}
}

// Security-review finding on the first commit: the install/delete path
// join must refuse a traversal subdirectory — fed to the deleter,
// "../../.." would rm -rf the join target as the OS user (their whole
// home). Applies to every flat-file app via the shared helper.
func TestAppInstallPath_RejectsTraversal(t *testing.T) {
	for _, bad := range []string{"..", "../..", "../../..", "a/../../b", "../outside"} {
		if _, err := appInstallPath("/home/u/domains/d/public_html", bad); err == nil {
			t.Errorf("appInstallPath accepted traversal subdirectory %q", bad)
		}
	}
	for _, ok := range []string{"", "paste", "tools/paste", "a/b/c"} {
		if _, err := appInstallPath("/home/u/domains/d/public_html", ok); err != nil {
			t.Errorf("appInstallPath rejected safe subdirectory %q: %v", ok, err)
		}
	}
}
