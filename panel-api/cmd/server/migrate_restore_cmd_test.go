package main

import "testing"

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
