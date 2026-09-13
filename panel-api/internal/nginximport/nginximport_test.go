package nginximport

import (
	"strings"
	"testing"
)

func ruleTypes(r Result) []string {
	var out []string
	for _, x := range r.Rules {
		out = append(out, x.Type)
	}
	return out
}

func TestConvert_SupportedShapes(t *testing.T) {
	snippet := `
# a migrated site
rewrite ^/old$ /new permanent;
add_header X-Robots-Tag "noindex" always;
location ~* \.(env|sql|bak)$ { deny all; }
location ~* \.(pdf|mp4)$ {
    expires 30d;
}
`
	res := Convert(snippet)
	got := ruleTypes(res)
	want := []string{"rewrite", "custom_header", "deny_paths", "static_cache"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rule types = %v, want %v (warnings: %+v)", got, want, res.Warnings)
	}

	// deny_paths extensions lifted from the alternation.
	dp := res.Rules[2]
	if strings.Join(dp.Extensions, "|") != "env|sql|bak" {
		t.Errorf("deny_paths extensions = %v", dp.Extensions)
	}
	// static_cache extensions + duration.
	sc := res.Rules[3]
	if strings.Join(sc.Extensions, "|") != "pdf|mp4" || sc.Duration != "30d" {
		t.Errorf("static_cache = %v dur=%q", sc.Extensions, sc.Duration)
	}
	// rewrite fields.
	rw := res.Rules[0]
	if rw.Pattern != "^/old$" || rw.Replacement != "/new" || rw.Flag != "permanent" {
		t.Errorf("rewrite = %+v", rw)
	}
	// custom_header with quoted value + always.
	ch := res.Rules[1]
	if ch.Name != "X-Robots-Tag" || ch.Value != "noindex" || ch.Always == nil || !*ch.Always {
		t.Errorf("custom_header = %+v", ch)
	}
}

func TestConvert_StaticCacheDropsInnerAddHeader(t *testing.T) {
	// A caching add_header inside the block must be dropped (JAB-70) and noted,
	// not imported — the static_cache rule renders `expires` only.
	res := Convert(`location ~* \.(woff2)$ {
    expires max;
    add_header Cache-Control "public, immutable";
}`)
	if len(res.Rules) != 1 || res.Rules[0].Type != "static_cache" {
		t.Fatalf("want one static_cache rule, got %v (warnings %+v)", ruleTypes(res), res.Warnings)
	}
	if len(res.Notes) == 0 {
		t.Errorf("expected a note about the dropped add_header")
	}
}

func TestConvert_SecurityDirectivesNeverBecomeRules(t *testing.T) {
	cases := []struct {
		name    string
		snippet string
	}{
		{"proxy_pass location", "location / { proxy_pass http://127.0.0.1:8080; }"},
		{"root inside allowed matcher", `location ~* \.(env)$ { root /etc; }`},
		{"alias inside allowed matcher", `location ~* \.(env)$ { alias /var/secret; }`},
		{"fastcgi_pass location", `location ~ \.php$ { fastcgi_pass unix:/run/php.sock; }`},
		{"bare return", "return 301 https://evil.example;"},
		{"try_files", "try_files $uri /index.php;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Convert(tc.snippet)
			if len(res.Rules) != 0 {
				t.Fatalf("expected NO rules, got %v", ruleTypes(res))
			}
			if len(res.Warnings) == 0 {
				t.Fatalf("expected a warning")
			}
			// The routing/file/proxy directives must be flagged Security.
			sawSec := false
			for _, w := range res.Warnings {
				if w.Security {
					sawSec = true
				}
			}
			if !sawSec {
				t.Errorf("expected a Security warning for %q, got %+v", tc.snippet, res.Warnings)
			}
		})
	}
}

func TestConvert_UnsupportedLocationMatcher(t *testing.T) {
	// A prefix location (not the extension-anchored regex) must not import — it
	// could shadow the panel's own locations.
	res := Convert("location /admin { deny all; }")
	if len(res.Rules) != 0 {
		t.Fatalf("prefix location must not import, got %v", ruleTypes(res))
	}
	if len(res.Warnings) != 1 || !res.Warnings[0].Security {
		t.Errorf("expected one Security warning, got %+v", res.Warnings)
	}
}

func TestConvert_IncompleteAndComments(t *testing.T) {
	res := Convert(`# only a comment

rewrite ^/a$ /b
`)
	if len(res.Rules) != 0 {
		t.Fatalf("an unterminated rewrite (no ;) must not import, got %v", ruleTypes(res))
	}
	if len(res.Warnings) != 1 {
		t.Errorf("expected one warning for the incomplete line, got %+v", res.Warnings)
	}
}

// A directive tacked onto a block's closing-brace line must not vanish — it
// must surface as a (security) warning, never a rule.
func TestConvert_TrailingDirectiveOnClosingLine(t *testing.T) {
	res := Convert("location ~* \\.(env)$ {\n    deny all;\n} proxy_pass http://127.0.0.1:9000;\nrewrite ^/foo$ /bar last;")
	// The deny block and the standalone rewrite still convert.
	if types := ruleTypes(res); strings.Join(types, ",") != "deny_paths,rewrite" {
		t.Fatalf("rule types = %v, want deny_paths,rewrite", types)
	}
	// The trailing proxy_pass must be flagged, not silently dropped.
	sawSecProxy := false
	for _, w := range res.Warnings {
		if w.Security && strings.Contains(w.Reason, "proxy_pass") {
			sawSecProxy = true
		}
	}
	if !sawSecProxy {
		t.Fatalf("trailing proxy_pass must produce a Security warning, got %+v", res.Warnings)
	}
}

func TestConvert_MixedDenyExpiresSkipped(t *testing.T) {
	res := Convert(`location ~* \.(env)$ { deny all; expires 30d; }`)
	if len(res.Rules) != 0 {
		t.Fatalf("ambiguous deny+expires must not import, got %v", ruleTypes(res))
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning")
	}
}
