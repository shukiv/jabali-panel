package phpbasedir

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		name, in   string
		tenant     bool
		want, errs string
	}{
		{name: "preset", in: "{DOCROOT}:{TMP}", tenant: true, want: "{DOCROOT}:{TMP}"},
		{name: "trimmed and deduped", in: " {DOCROOT}:{TMP}:{DOCROOT} ", tenant: true, want: "{DOCROOT}:{TMP}"},
		{name: "tenant path in own home", in: "{DOCROOT}:/home/alice/lib", tenant: true, want: "{DOCROOT}:/home/alice/lib"},
		{name: "tenant own home", in: "/home/alice", tenant: true, want: "/home/alice"},
		{name: "tenant path outside home", in: "{DOCROOT}:/usr/share/php", tenant: true, errs: "outside your home"},
		{name: "tenant sibling-prefixed home", in: "/home/alicex", tenant: true, errs: "outside your home"},
		{name: "admin path outside home", in: "{DOCROOT}:/usr/share/php", want: "{DOCROOT}:/usr/share/php"},
		{name: "admin root", in: "/", errs: "lift the restriction"},
		{name: "admin /home", in: "/home", errs: "other users' home"},
		{name: "admin other home", in: "/home/bob", errs: "another user's home"},
		{name: "admin sibling-prefixed home", in: "/home/alicex/x", errs: "another user's home"},
		{name: "admin /root", in: "/root/.ssh", errs: "/root"},
		{name: "admin /proc", in: "/proc", errs: "/proc"},
		{name: "relative", in: "lib", errs: "absolute path"},
		{name: "dotdot", in: "/home/alice/../bob", errs: "clean path"},
		{name: "trailing slash", in: "/home/alice/", errs: "clean path"},
		{name: "nginx variable", in: "/home/alice/$x", tenant: true, errs: "absolute path"},
		{name: "quote", in: `/home/alice/"`, tenant: true, errs: "absolute path"},
		{name: "newline", in: "/home/alice\nallow_url_fopen=1", tenant: true, errs: "absolute path"},
		{name: "unknown token", in: "{HOME}", errs: "absolute path"},
		{name: "empty entry", in: "{DOCROOT}::{TMP}", errs: "empty entry"},
		{name: "empty", in: "  ", errs: "empty"},
		{name: "too many", in: strings.Repeat("{TMP}:", MaxEntries) + "{TMP}", errs: "entries"},
		{name: "too long", in: "/home/alice/" + strings.Repeat("a", MaxLen), errs: "longer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in, "alice", tc.tenant)
			if tc.errs != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errs) {
					t.Fatalf("Normalize(%q) = %q, %v; want error containing %q", tc.in, got, err, tc.errs)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Normalize(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
	if _, err := Normalize("{DOCROOT}", "../bob", false); err == nil {
		t.Fatal("Normalize accepted an invalid owner")
	}
}

func TestExpand(t *testing.T) {
	const doc = "/home/alice/domains/a.test/public_html"
	got, err := Expand("{DOCROOT}:{TMP}:{DOCROOT}", "alice", doc)
	want := doc + ":/tmp:/var/tmp:/run/mysqld/mysqld.sock:/run/jabali-wp-purge"
	if err != nil || got != want {
		t.Fatalf("Expand = %q, %v; want %q", got, err, want)
	}
	got, err = Expand("{WEBSPACEROOT}:/usr/share/php", "alice", doc)
	want = "/home/alice:/usr/share/php:/run/mysqld/mysqld.sock:/run/jabali-wp-purge"
	if err != nil || got != want {
		t.Fatalf("Expand = %q, %v; want %q", got, err, want)
	}
	for _, tc := range []struct{ value, docRoot string }{
		{"{DOCROOT}", "/home/bob/domains/b.test/public_html"}, // docroot in another home
		{"{DOCROOT}", "/home/alicex/public_html"},
		{"{DOCROOT}", "/home/alice/../bob"},
		{"{DOCROOT}", ""},
		{"/home/bob", doc}, // a stored value that bypassed the panel
		{"/", doc},
		{"{DOCROOT}\nallow_url_fopen=1", doc},
	} {
		if got, err := Expand(tc.value, "alice", tc.docRoot); err == nil {
			t.Errorf("Expand(%q, docroot %q) = %q, want an error", tc.value, tc.docRoot, got)
		}
	}
}

func TestSafePinValue(t *testing.T) {
	if !SafePinValue("/home/alice:/run/mysqld/mysqld.sock:/tmp:/var/tmp:/run/jabali-wp-purge") {
		t.Fatal("the pool template's open_basedir must be pinnable")
	}
	for _, v := range []string{"", "/home/alice:", "/home/$u", `/home/a"`, "/home/a\n/x", "relative"} {
		if SafePinValue(v) {
			t.Errorf("SafePinValue(%q) = true", v)
		}
	}
}
