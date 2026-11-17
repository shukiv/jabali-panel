package main

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
)

func TestCpmoveSourceUser(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"cpmove-newpuzzleans.tar.gz", "newpuzzleans"},
		{"/home/u/domains/x/public_html/cpmove-newpuzzleans.tar.gz", "newpuzzleans"},
		{"cpmove-bob123.tar.gz", "bob123"},
		{"cpmove-user_with-dots.v2.tar.gz", "user_with-dots.v2"},
		{"backup-1.2.3_bob.tar.gz", ""}, // pkgacct shape — not auto-derived
		{"cpmove-bob.tar", ""},          // not .tar.gz
		{"random.tar.gz", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := cpmoveSourceUser(c.path); got != c.want {
			t.Errorf("cpmoveSourceUser(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// JAB-25: `jabali migrate restore --hestiacp` derives the source user from a
// HestiaCP v-backup-user filename, and rejects (returns "") an archive whose
// name has no recognizable user so the CLI can demand --source-user.
func TestHestiaSourceUser(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/backup/itflowapp.2026-07-05_16-10-38.tar", "itflowapp"},
		{"/backup/itflowapp.2026-07-05_16-10-38.tar.gz", "itflowapp"},
		{"itflow-app_prod.2026-01-02_03-04-05.tar", "itflow-app_prod"},
		// Renamed sample without the timestamp — ambiguous, must return "".
		{"/home/shuki/hestia-sample.tar", ""},
		// cPanel name is not a Hestia backup name.
		{"cpmove-foo.tar.gz", ""},
		{"random.tar", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := hestiaSourceUser(c.path); got != c.want {
			t.Errorf("hestiaSourceUser(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// `jabali migrate restore --directadmin` takes the source account from the
// backup's own backup/user.conf, whatever the file is named, and refuses a
// --source-user that names another account.
func TestDANativeSourceUser(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, files map[string]string, order []string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(f)
		for _, n := range order {
			body := files[n]
			if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	good := write("renamed.tar", map[string]string{
		"backup/user.conf": "username=coil197\ndomain=197.co.il\n",
		"backup/.shadow":   "$6$x$y\n",
	}, []string{"backup/user.conf", "backup/.shadow"})

	if got, err := daNativeSourceUser(good, ""); err != nil || got != "coil197" {
		t.Errorf("no --source-user: got %q, %v", got, err)
	}
	if got, err := daNativeSourceUser(good, "coil197"); err != nil || got != "coil197" {
		t.Errorf("matching --source-user: got %q, %v", got, err)
	}
	if _, err := daNativeSourceUser(good, "someoneelse"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("mismatched --source-user accepted: %v", err)
	}

	cpmove := write("cpmove-bob.tar", map[string]string{"cpmove-bob/cp/bob": "USER=bob\n"}, []string{"cpmove-bob/cp/bob"})
	if _, err := daNativeSourceUser(cpmove, "bob"); err == nil {
		t.Error("a cpmove accepted as a DirectAdmin backup")
	}
	bad := write("bad.tar", map[string]string{"backup/user.conf": "username=../root\n"}, []string{"backup/user.conf"})
	if _, err := daNativeSourceUser(bad, ""); err == nil {
		t.Error("an invalid account name accepted")
	}
}

// A WordPress in a subdirectory of a docroot (DirectAdmin's shop at
// public_html/<dir>/) is repointed and its index.html shadow checked, like a
// docroot's own.
func TestAppConfigRoots(t *testing.T) {
	docroots := []string{"/home/u/domains/a.example/public_html"}
	got := appConfigRoots(docroots, &cpanel.ParsedTarball{ExtraAppRoots: []string{"/home/u/domains/a.example/public_html/shop"}})
	want := []string{"/home/u/domains/a.example/public_html", "/home/u/domains/a.example/public_html/shop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v", got)
	}
	if len(docroots) != 1 {
		t.Error("the docroot list was changed")
	}
}
