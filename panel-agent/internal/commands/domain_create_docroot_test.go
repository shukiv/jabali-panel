package commands

import "testing"

// A doc_root is written verbatim into `root <path>;` in the vhost. Any
// directive terminator, block brace, comment, variable, quote, whitespace or
// control byte there lets a value add directives the tenant grammar forbids.
func TestValidateDocRootForNginx(t *testing.T) {
	ok := []string{
		"/home/u/domains/example.com/public_html",
		"/home/u/domains/example.com/public_html/",
		"/home/u/domains/my-site.example.com/app.v2/web",
	}
	bad := []string{
		"/home/u/domains/example.com/public_html; include /home/u/x.conf",
		"/home/u/domains/example.com/public_html;}",
		"/home/u/domains/example.com/public_html {",
		"/home/u/domains/example.com/public_html#x",
		"/home/u/domains/example.com/$host",
		"/home/u/domains/example.com/a\"b",
		"/home/u/domains/example.com/a'b",
		"/home/u/domains/example.com/a\\b",
		"/home/u/domains/example.com/a b",
		"/home/u/domains/example.com/a\tb",
		"/home/u/domains/example.com/a\nb",
		"/home/u/domains/example.com/a\x7fb",
		"/home/u/domains/example.com/../../etc",
	}
	for _, p := range ok {
		if err := validateDocRootForNginx(p); err != nil {
			t.Errorf("validateDocRootForNginx(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range bad {
		if err := validateDocRootForNginx(p); err == nil {
			t.Errorf("validateDocRootForNginx(%q) = nil, want error", p)
		}
	}
}
