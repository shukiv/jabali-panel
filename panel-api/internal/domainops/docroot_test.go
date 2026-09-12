package domainops

import (
	"errors"
	"testing"
)

// TestValidateDocumentRoot covers the admin/operator floor: empty defaults,
// anywhere under the owner home passes, outside the home or a ".." traversal is
// refused. This is the confinement the CLI gained (an arbitrary --doc-root was
// stored unchecked and then mkdir'd + served by the reconciler).
func TestValidateDocumentRoot(t *testing.T) {
	const user, dom = "alice", "example.com"
	cases := []struct {
		name    string
		docRoot string
		want    error
	}{
		{"empty is default", "", nil},
		{"domain public_html passes", "/home/alice/domains/example.com/public_html", nil},
		{"elsewhere under home passes", "/home/alice/public", nil},
		{"outside home rejected", "/var/www/html", ErrDocRootOutsideHome},
		{"another user home rejected", "/home/bob/domains/x/public_html", ErrDocRootOutsideHome},
		{"traversal under home rejected", "/home/alice/../bob/secret", ErrDocRootTraversal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateDocumentRoot(c.docRoot, user, dom); !errors.Is(err, c.want) {
				t.Fatalf("ValidateDocumentRoot(%q): want %v, got %v", c.docRoot, c.want, err)
			}
		})
	}
}

// TestValidateDocumentRoot_Order pins the REST admin path's precedence: a path
// outside the home is reported as outside-home even when it also contains "..",
// because the prefix check runs first. Reordering would change which message the
// REST adapter puts on the wire.
func TestValidateDocumentRoot_Order(t *testing.T) {
	if err := ValidateDocumentRoot("/var/../etc/passwd", "alice", "example.com"); !errors.Is(err, ErrDocRootOutsideHome) {
		t.Fatalf("want ErrDocRootOutsideHome (prefix checked before traversal), got %v", err)
	}
}

// TestValidateTenantDocumentRoot covers the stricter non-admin confinement: the
// cleaned path must stay inside the domain's own tree; a sibling domain, the
// parent directory, or anywhere outside the home is refused, and a ".." is
// rejected before Clean.
func TestValidateTenantDocumentRoot(t *testing.T) {
	const user, dom = "alice", "example.com"
	base := "/home/alice/domains/example.com"
	cases := []struct {
		name    string
		docRoot string
		want    error
	}{
		{"empty is default", "", nil},
		{"base itself passes", base, nil},
		{"subdir passes", base + "/public", nil},
		{"dot-slash normalizes and passes", base + "/./public", nil},
		{"traversal rejected", base + "/../other", ErrDocRootTraversal},
		{"sibling domain rejected", "/home/alice/domains/other.com/public_html", ErrDocRootOutsideDomain},
		{"parent of domain rejected", "/home/alice/domains", ErrDocRootOutsideDomain},
		{"outside home rejected", "/var/www", ErrDocRootOutsideDomain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateTenantDocumentRoot(c.docRoot, user, dom); !errors.Is(err, c.want) {
				t.Fatalf("ValidateTenantDocumentRoot(%q): want %v, got %v", c.docRoot, c.want, err)
			}
		})
	}
}
