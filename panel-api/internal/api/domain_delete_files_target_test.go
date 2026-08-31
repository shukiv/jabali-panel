package api

import "testing"

// GH #1382 follow-up: "also delete the files" must remove the whole per-domain
// folder, not just its public_html — but never a docroot that lives outside it.
func TestDomainDeleteFilesTarget(t *testing.T) {
	cases := []struct {
		name     string
		user     string
		domain   string
		docroot  string
		wantPath string
	}{
		{
			name:     "default docroot → whole domain folder",
			user:     "alice", domain: "example.com",
			docroot:  "/home/alice/domains/example.com/public_html",
			wantPath: "/home/alice/domains/example.com",
		},
		{
			name:     "custom docroot under the domain folder → whole domain folder",
			user:     "alice", domain: "example.com",
			docroot:  "/home/alice/domains/example.com/current/web",
			wantPath: "/home/alice/domains/example.com",
		},
		{
			name:     "docroot outside the domain folder → docroot unchanged",
			user:     "alice", domain: "example.com",
			docroot:  "/home/alice/public_html",
			wantPath: "/home/alice/public_html",
		},
		{
			name:     "prefix-sibling domain name is not a match",
			user:     "alice", domain: "example.com",
			docroot:  "/home/alice/domains/example.com.bak/public_html",
			wantPath: "/home/alice/domains/example.com.bak/public_html",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainDeleteFilesTarget(tc.user, tc.domain, tc.docroot)
			if got != tc.wantPath {
				t.Fatalf("target = %q, want %q", got, tc.wantPath)
			}
		})
	}
}
