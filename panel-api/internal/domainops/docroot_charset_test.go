package domainops

import (
	"errors"
	"testing"
)

// The tenant write path accepts only a plain path: letters, digits, dot,
// underscore, slash and hyphen. Anything else can change the vhost it is
// rendered into.
func TestValidateTenantDocumentRoot_Charset(t *testing.T) {
	const base = "/home/u/domains/example.com"
	if err := ValidateTenantDocumentRoot(base+"/public_html", "u", "example.com"); err != nil {
		t.Fatalf("plain path rejected: %v", err)
	}
	for _, p := range []string{
		base + "/public_html; include /home/u/x.conf",
		base + "/public_html}",
		base + "/$host",
		base + "/a b",
		base + "/a\"b",
		base + "/a#b",
	} {
		if err := ValidateTenantDocumentRoot(p, "u", "example.com"); !errors.Is(err, ErrDocRootBadChars) {
			t.Errorf("ValidateTenantDocumentRoot(%q) = %v, want ErrDocRootBadChars", p, err)
		}
	}
}
