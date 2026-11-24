package api

import (
	"fmt"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidatePageRedirects_BulkRules covers what a migrated site's
// .htaccess needs (hundreds of entries, 303/307/308, same-site paths) and
// what would break the whole vhost (two entries on one nginx location).
func TestValidatePageRedirects_BulkRules(t *testing.T) {
	many := func(n int) models.PageRedirects {
		out := make(models.PageRedirects, n)
		for i := range out {
			out[i] = models.PageRedirect{Source: fmt.Sprintf("/s%d", i), Destination: "https://ok.example/x?aid=1", Type: "307", KeepQuery: true, TrailingSlash: true}
		}
		return out
	}
	if err := validatePageRedirects(many(maxPageRedirects)); err != nil {
		t.Fatalf("%d redirects must pass: %v", maxPageRedirects, err)
	}
	if err := validatePageRedirects(many(maxPageRedirects + 1)); err == nil {
		t.Fatalf("%d redirects must be refused", maxPageRedirects+1)
	}

	off := false
	ok := []models.PageRedirect{
		{Source: "/a", Destination: "/he/new-page", Type: "303"},
		{Source: "/a", Destination: "https://ok.example/", Type: "301", Wildcard: true}, // ^~ /a is not = /a
		{Source: "/b", Destination: "https://ok.example/", Type: "308", Active: &off},
		{Source: "/b", Destination: "https://ok.example/", Type: "308"}, // the other /b is off
	}
	if err := validatePageRedirects(ok); err != nil {
		t.Fatalf("valid set refused: %v", err)
	}

	for name, prs := range map[string]models.PageRedirects{
		"same exact path":        {{Source: "/x", Destination: "/y", Type: "301"}, {Source: "/x", Destination: "/z", Type: "302"}},
		"slash variant":          {{Source: "/x", Destination: "/y", Type: "301", TrailingSlash: true}, {Source: "/x/", Destination: "/z", Type: "301"}},
		"same wildcard":          {{Source: "/w", Destination: "https://ok.example", Type: "301", Wildcard: true}, {Source: "/w", Destination: "https://ok.example/2", Type: "302", Wildcard: true}},
		"protocol-relative path": {{Source: "/x", Destination: "//evil.example/", Type: "301"}},
		"path with space":        {{Source: "/x", Destination: "/a b", Type: "301"}},
		"path with $":            {{Source: "/x", Destination: "/a$host", Type: "301"}},
		"wildcard 307":           {{Source: "/x", Destination: "https://ok.example", Type: "307", Wildcard: true}},
		"wildcard keep query":    {{Source: "/x", Destination: "https://ok.example", Type: "301", Wildcard: true, KeepQuery: true}},
		"wildcard slash":         {{Source: "/x", Destination: "https://ok.example", Type: "301", Wildcard: true, TrailingSlash: true}},
		"javascript dest":        {{Source: "/x", Destination: "javascript:alert(1)", Type: "301"}},
	} {
		if err := validatePageRedirects(prs); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestDropRestoredPageRedirects_DuplicatePath: a restored list keeps the
// first of two entries on one path (as Apache would), instead of carrying a
// duplicate location that fails nginx -t for the whole vhost.
func TestDropRestoredPageRedirects_DuplicatePath(t *testing.T) {
	row := &models.Domain{PageRedirects: models.PageRedirects{
		{Source: "/x", Destination: "/first", Type: "301", TrailingSlash: true},
		{Source: "/x/", Destination: "/second", Type: "301"},
		{Source: "/y", Destination: "/third", Type: "307"},
	}}
	warnings := dropRestoredPageRedirects(row)
	if len(row.PageRedirects) != 2 || row.PageRedirects[0].Destination != "/first" || row.PageRedirects[1].Destination != "/third" {
		t.Fatalf("kept %+v", row.PageRedirects)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "already covers") {
		t.Fatalf("warnings %q", warnings)
	}
}
