package plesk

import (
	"os"
	"strings"
	"testing"
)

// The mail rsync runs as ROOT over SSH, and every Plesk subscription's mail
// sits side by side under /var/qmail/mailnames/. So the set of source paths the
// migration may read must be derived from the SOURCE, never from the staged
// manifest — the same reasoning as the docroot guard after GH #746.
//
// These pin the query shape, because that is what decides which paths are
// considered ours. A query that dropped the domain join would allowlist every
// mailbox on the host.
func TestAuthoritativeMaildirsQueryIsScopedToTheDomain(t *testing.T) {
	t.Parallel()
	src := readSourceFile(t, "mail.go")

	for _, want := range []string{
		"FROM mail m JOIN domains d ON m.dom_id=d.id",
		"WHERE d.name=",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("AuthoritativeMaildirs must scope its psa query with %q — without it "+
				"the allowlist would cover every mailbox on the source host", want)
		}
	}
	if !strings.Contains(src, "sqlQuote(dom)") {
		t.Error("the domain must go through sqlQuote — it reaches a shell-quoted `plesk db` invocation")
	}
}

// A mail name is used to build a filesystem path that root then rsyncs from.
func TestAuthoritativeMaildirsRejectsPathShapedMailNames(t *testing.T) {
	t.Parallel()
	src := readSourceFile(t, "mail.go")
	if !strings.Contains(src, `strings.ContainsAny(local, "/\\")`) || !strings.Contains(src, `strings.Contains(local, "..")`) {
		t.Error("a psa mail_name must never be allowed to contain a path separator or traversal — " +
			"it is concatenated into a path that root rsyncs from")
	}
}

// The Maildir layout is Plesk's, not a guess: <root>/<domain>/<local>/Maildir.
func TestAuthoritativeMaildirsBuildsThePleskLayout(t *testing.T) {
	t.Parallel()
	src := readSourceFile(t, "mail.go")
	if !strings.Contains(src, `pleskMailnamesRoot + "/" + dom + "/" + local + "/Maildir"`) {
		t.Error("Maildir path must be <mailnames root>/<domain>/<local>/Maildir")
	}
	if !strings.Contains(src, `const pleskMailnamesRoot = "/var/qmail/mailnames"`) {
		t.Error("the mailnames root constant moved; the rsync guard's prefix check depends on it")
	}
}

// readSourceFile reads a file from this package. These tests assert on the
// SQL and path construction rather than round-tripping a live Plesk box,
// because the property that matters — "the allowlist comes from the source and
// is scoped to this subscription" — is a property of the query text.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
