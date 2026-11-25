package redirects

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestCompile_HealsLegacyRedirectType guards the JAB-318 render-time heal: the
// old `jabali domain set` CLI persisted redirect_all_type="permanent"/"temporary"
// (invalid nginx return codes), which this file rendered verbatim as
// `return permanent …;` — breaking nginx -t and the reload. Compile must now map
// them to their numeric codes so existing rows and restored backups converge
// without a data migration. Falsify by removing the redirectAllCode switch → the
// first two cases render `return permanent/temporary …;`.
func TestCompile_HealsLegacyRedirectType(t *testing.T) {
	cases := []struct{ stored, wantCode string }{
		{"permanent", "301"},
		{"temporary", "302"},
		{"Permanent", "301"}, // case-insensitive
		{"301", "301"},       // already-numeric passes through
		{"308", "308"},
	}
	for _, tc := range cases {
		t.Run(tc.stored, func(t *testing.T) {
			got := Compile(&models.Domain{
				RedirectAllTo:   str("https://new.com"),
				RedirectAllType: str(tc.stored),
			})
			want := "    return " + tc.wantCode + " \"https://new.com\";\n"
			if got != want {
				t.Errorf("Compile() = %q, want %q", got, want)
			}
		})
	}
}

func TestCompile(t *testing.T) {
	tests := []struct {
		name     string
		domain   *models.Domain
		expected string
	}{
		{
			name:     "nil domain",
			domain:   nil,
			expected: "",
		},
		{
			name:     "no redirects configured",
			domain:   &models.Domain{},
			expected: "",
		},
		{
			name: "whole-domain 301",
			domain: &models.Domain{
				RedirectAllTo:   str("https://new.com"),
				RedirectAllType: str("301"),
			},
			expected: `    return 301 "https://new.com";
`,
		},
		{
			name: "whole-domain defaults to 301 when type is nil",
			domain: &models.Domain{
				RedirectAllTo: str("https://new.com"),
			},
			expected: `    return 301 "https://new.com";
`,
		},
		{
			name: "whole-domain 302 output",
			domain: &models.Domain{
				RedirectAllTo:   str("https://new.com"),
				RedirectAllType: str("302"),
			},
			expected: `    return 302 "https://new.com";
`,
		},
		{
			name: "whole-domain SUPERSEDES page redirects",
			domain: &models.Domain{
				RedirectAllTo:   str("https://new.com"),
				RedirectAllType: str("301"),
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old", Destination: "https://other.com", Type: "301"},
				},
			},
			expected: `    return 301 "https://new.com";
`,
		},
		{
			name: "single page redirect exact-match output",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old", Destination: "https://new.com/page", Type: "301"},
				},
			},
			expected: `    location = /old {
        return 301 "https://new.com/page";
    }
`,
		},
		{
			name: "embedded double-quote in destination (escaped)",
			domain: &models.Domain{
				RedirectAllTo:   str(`https://new.com/path?q="value"`),
				RedirectAllType: str("301"),
			},
			expected: `    return 301 "https://new.com/path?q=\"value\"";
`,
		},
		{
			name: "nginx $vars in destination preserved",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old", Destination: "https://new.com/$request_uri", Type: "301"},
				},
			},
			expected: `    location = /old {
        return 301 "https://new.com/$request_uri";
    }
`,
		},
		{
			name: "multiple page redirects in order",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old1", Destination: "https://new.com/page1", Type: "301"},
					models.PageRedirect{Source: "/old2", Destination: "https://new.com/page2", Type: "302"},
				},
			},
			expected: `    location = /old1 {
        return 301 "https://new.com/page1";
    }
    location = /old2 {
        return 302 "https://new.com/page2";
    }
`,
		},
		{
			name: "backslash escaping in destination",
			domain: &models.Domain{
				RedirectAllTo:   str(`https://new.com\path`),
				RedirectAllType: str("301"),
			},
			expected: `    return 301 "https://new.com\\path";
`,
		},
		{
			name: "both quote and backslash in destination",
			domain: &models.Domain{
				RedirectAllTo:   str(`https://new.com\path?q="value"`),
				RedirectAllType: str("301"),
			},
			expected: `    return 301 "https://new.com\\path?q=\"value\"";
`,
		},
		{
			name: "location path with spaces (quoted)",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/my path", Destination: "https://new.com/page", Type: "301"},
				},
			},
			expected: `    location = "/my path" {
        return 301 "https://new.com/page";
    }
`,
		},
		{
			name: "empty RedirectAllTo cleared (nil after trim)",
			domain: &models.Domain{
				RedirectAllTo: str(""),
			},
			expected: "",
		},
		// v2 features: Active, Wildcard
		{
			name: "active nil means active (backwards compat)",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old", Destination: "https://new.com/page", Type: "301", Active: nil},
				},
			},
			expected: `    location = /old {
        return 301 "https://new.com/page";
    }
`,
		},
		{
			name: "active false skips entry",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old1", Destination: "https://new.com/page1", Type: "301", Active: ptr(false)},
					models.PageRedirect{Source: "/old2", Destination: "https://new.com/page2", Type: "301", Active: ptr(true)},
				},
			},
			expected: `    location = /old2 {
        return 301 "https://new.com/page2";
    }
`,
		},
		{
			name: "wildcard 301",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old-prefix", Destination: "https://new.com/new", Type: "301", Wildcard: true},
				},
			},
			expected: `    location ^~ /old-prefix {
        rewrite ^/old-prefix/?(.*)$ "https://new.com/new/$1" permanent;
    }
`,
		},
		{
			name: "wildcard 302",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/old-prefix", Destination: "https://new.com/new", Type: "302", Wildcard: true},
				},
			},
			expected: `    location ^~ /old-prefix {
        rewrite ^/old-prefix/?(.*)$ "https://new.com/new/$1" redirect;
    }
`,
		},
		{
			name: "wildcard with special regex chars in source (escaped)",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/api.v2", Destination: "https://new.com/api", Type: "301", Wildcard: true},
				},
			},
			expected: `    location ^~ /api.v2 {
        rewrite ^/api\.v2/?(.*)$ "https://new.com/api/$1" permanent;
    }
`,
		},
		{
			name: "ordering preserved with mixed active/inactive",
			domain: &models.Domain{
				PageRedirects: models.PageRedirects{
					models.PageRedirect{Source: "/first", Destination: "https://new.com/1", Type: "301", Active: ptr(true)},
					models.PageRedirect{Source: "/second", Destination: "https://new.com/2", Type: "301", Active: ptr(false)},
					models.PageRedirect{Source: "/third", Destination: "https://new.com/3", Type: "301", Active: ptr(true)},
				},
			},
			expected: `    location = /first {
        return 301 "https://new.com/1";
    }
    location = /third {
        return 301 "https://new.com/3";
    }
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compile(tt.domain)
			if got != tt.expected {
				t.Errorf("Compile() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}

func str(s string) *string {
	return &s
}

// TestCompile_KeepQueryAndTrailingSlash pins what an Apache `Redirect` imports
// to: /slug and /slug/ both answer, and the request's query string is carried
// over after '?' (or '&' when the destination already has one). Falsify by
// dropping the KeepQuery branch of writeExact (query lost) or the
// TrailingSlash entry of ExactSources (/slug/ falls through to the site).
func TestCompile_KeepQueryAndTrailingSlash(t *testing.T) {
	got := Compile(&models.Domain{PageRedirects: models.PageRedirects{
		{Source: "/go", Destination: "https://shop.example/p?aid=7", Type: "307", KeepQuery: true, TrailingSlash: true},
		{Source: "/he/", Destination: "/he/new", Type: "301", KeepQuery: true, TrailingSlash: true},
		{Source: "/a;b", Destination: `https://x.example/"q"`, Type: "308"},
	}})
	want := `    location = /go {
        set $jabali_rq "";
        if ($args != "") { set $jabali_rq "&$args"; }
        return 307 "https://shop.example/p?aid=7$jabali_rq";
    }
    location = /go/ {
        set $jabali_rq "";
        if ($args != "") { set $jabali_rq "&$args"; }
        return 307 "https://shop.example/p?aid=7$jabali_rq";
    }
    location = /he/ {
        set $jabali_rq "";
        if ($args != "") { set $jabali_rq "?$args"; }
        return 301 "/he/new$jabali_rq";
    }
    location = "/a;b" {
        return 308 "https://x.example/\"q\"";
    }
`
	if got != want {
		t.Errorf("Compile() =\n%s\nwant\n%s", got, want)
	}
}

// GH #2591: www.<domain> answers 301 to <domain> when RedirectWWW is on, ahead
// of the page redirects; a whole-domain redirect still wins, and a domain whose
// own name starts with www. gets nothing (there is no www.www.).
func TestCompile_RedirectWWW(t *testing.T) {
	wwwBlock := "    if ($host = \"www.example.com\") {\n        return 301 \"$scheme://example.com$request_uri\";\n    }\n"
	page := []models.PageRedirect{{Source: "/old", Destination: "/new", Type: "301"}}

	got := Compile(&models.Domain{Name: "example.com", RedirectWWW: true, PageRedirects: page})
	if !strings.HasPrefix(got, wwwBlock) || !strings.Contains(got, "location = /old") {
		t.Fatalf("www block must come first, then the page redirects:\n%s", got)
	}
	if got := Compile(&models.Domain{Name: "example.com"}); strings.Contains(got, "$host") {
		t.Fatalf("off must emit no www redirect:\n%s", got)
	}
	if got := Compile(&models.Domain{Name: "www.example.com", RedirectWWW: true}); got != "" {
		t.Fatalf("a www. name must emit nothing:\n%s", got)
	}
	all := Compile(&models.Domain{Name: "example.com", RedirectWWW: true, RedirectAllTo: str("https://new.example")})
	if strings.Contains(all, "$host") || !strings.Contains(all, "return 301 \"https://new.example\"") {
		t.Fatalf("a whole-domain redirect wins:\n%s", all)
	}
}
