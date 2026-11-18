package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

// SECURITY (nginx config injection): every wp_subdirs entry is rendered
// verbatim into the vhost, so the agent re-validates it whatever the panel sent.
func TestSanitizeWPSubdir(t *testing.T) {
	ok := map[string]string{
		"shop":                                "/shop",
		"/shop/":                              "/shop",
		" blog ":                              "/blog",
		"a_long_folder_name_with_underscores": "/a_long_folder_name_with_underscores",
		"shop/eu":                             "/shop/eu",
		"Shop.v2":                             "/Shop.v2",
	}
	for in, want := range ok {
		if got, good := sanitizeWPSubdir(in); !good || got != want {
			t.Errorf("sanitizeWPSubdir(%q) = %q,%v; want %q,true", in, got, good, want)
		}
	}
	bad := []string{
		"", "/", "..", "../etc", "a/../b", "./a", ".hidden", "a//b",
		"shop { return 200; }", "shop;", "a b", `sh"op`, "sh'op", "a\nb", "$uri", "shop~",
		"a/b/c/d/e", strings.Repeat("x", 65),
	}
	for _, in := range bad {
		if got, good := sanitizeWPSubdir(in); good {
			t.Errorf("sanitizeWPSubdir(%q) accepted as %q; want refused", in, got)
		}
	}
}

func TestWPSubdirRoutes(t *testing.T) {
	old := nginxJabaliDir
	nginxJabaliDir = t.TempDir()
	t.Cleanup(func() { nginxJabaliDir = old })

	cases := []struct {
		name   string
		subs   []string
		blocks []string
		want   []string
	}{
		{"none", nil, nil, nil},
		{"one", []string{"shop"}, nil, []string{"/shop"}},
		{"dedupe and sort", []string{"/shop/", "blog", "shop"}, nil, []string{"/blog", "/shop"}},
		{"invalid dropped", []string{"shop", "x;y"}, nil, []string{"/shop"}},
		{"nested installs both route", []string{"shop", "shop/eu"}, nil, []string{"/shop", "/shop/eu"}},
		// Same path: a second prefix location is a duplicate (nginx -t fails).
		{"folder privacy on the same folder", []string{"shop"},
			[]string{"location ^~ /shop/ {\n    auth_basic \"x\";\n}"}, nil},
		{"rule on the same folder, unquoted, no ^~", []string{"shop"},
			[]string{"location /shop/ { return 301 /; }"}, nil},
		{"quoted location", []string{"shop"}, []string{`location "/shop/" { }`}, nil},
		// A protected parent must keep protecting: a longer prefix would win.
		{"folder privacy on a parent", []string{"a/b"},
			[]string{"location ^~ /a/ { auth_basic \"x\"; }"}, nil},
		{"parent without trailing slash", []string{"shop"}, []string{"location ^~ /sh { }"}, nil},
		// A child, an exact match or a regex never collides.
		{"privacy on a child folder", []string{"shop"},
			[]string{"location ^~ /shop/private/ { auth_basic \"x\"; }"}, []string{"/shop"}},
		{"exact location", []string{"shop"}, []string{"location = /shop/ { }"}, []string{"/shop"}},
		{"regex location", []string{"shop"}, []string{"location ~ ^/shop/ { }"}, []string{"/shop"}},
		{"sibling folder", []string{"shop"}, []string{"location ^~ /blog/ { }"}, []string{"/shop"}},
		{"root location ignored here", []string{"shop"}, []string{"location / { }"}, []string{"/shop"}},
		{"commented out", []string{"shop"}, []string{"# location ^~ /shop/ {"}, []string{"/shop"}},
	}
	for _, c := range cases {
		if got := wpSubdirRoutes("example.com", c.subs, c.blocks...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: wpSubdirRoutes = %v, want %v", c.name, got, c.want)
		}
	}
}

// An app's own rewrite snippet (included from /etc/nginx/jabali/<domain>/)
// declares `location ^~ /<sub>/`; a WordPress route for the same folder would
// be a duplicate location.
func TestWPSubdirRoutes_AppSnippetCollision(t *testing.T) {
	old := nginxJabaliDir
	nginxJabaliDir = t.TempDir()
	t.Cleanup(func() { nginxJabaliDir = old })
	dir := filepath.Join(nginxJabaliDir, "example.com")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drupal-shop.conf"), []byte("location ^~ /shop/ {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := wpSubdirRoutes("example.com", []string{"shop", "blog"})
	if want := []string{"/blog"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wpSubdirRoutes = %v, want %v", got, want)
	}
	// Another domain's snippets are not this domain's.
	if got := wpSubdirRoutes("other.example", []string{"shop"}); !reflect.DeepEqual(got, []string{"/shop"}) {
		t.Errorf("other domain: wpSubdirRoutes = %v, want [/shop]", got)
	}
}

// Wire contract with the panel's reconciler, which sends params["wp_subdirs"].
func TestDomainCreateParams_WPSubdirsKey(t *testing.T) {
	var p domainCreateParams
	if err := json.Unmarshal([]byte(`{"domain":"example.com","wp_subdirs":["/shop","/blog"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.WPSubdirs, []string{"/shop", "/blog"}) {
		t.Fatalf("WPSubdirs = %v, want [/shop /blog]", p.WPSubdirs)
	}
}

func renderWPSubdirVhost(t *testing.T, vd vhostData) string {
	t.Helper()
	tmpl := template.Must(template.New("v").Parse(vhostTemplate))
	var b bytes.Buffer
	if err := tmpl.Execute(&b, vd); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestVhostTemplate_WPSubdirRoute(t *testing.T) {
	base := func() vhostData {
		return vhostData{
			Domain: "example.com", DocRoot: "/home/u/domains/example.com/public_html",
			HasPHP: true, PHPVersion: "8.4", Username: "u", IsEnabled: true,
		}
	}
	route := "    location /shop/ {\n        try_files $uri $uri/ /shop/index.php?$query_string;\n    }\n"

	vd := base()
	vd.WPSubdirRoutes = []string{"/shop", "/shop/eu"}
	out := renderWPSubdirVhost(t, vd)
	if !strings.Contains(out, route) {
		t.Errorf("missing the /shop/ route:\n%s", out)
	}
	if !strings.Contains(out, "try_files $uri $uri/ /shop/eu/index.php?$query_string;") {
		t.Error("missing the nested /shop/eu/ route")
	}
	if strings.Contains(out, "location ^~ /shop/") {
		t.Error("route must be a plain prefix location: ^~ would stop .php reaching the regex PHP location")
	}
	// The site root keeps its own front controller.
	if !strings.Contains(out, "try_files $uri $uri/ /index.php?$query_string;") {
		t.Error("root location / must keep its /index.php fallback")
	}

	// Without PHP there is no index.php to fall back to.
	vd = base()
	vd.HasPHP = false
	vd.WPSubdirRoutes = []string{"/shop"}
	if strings.Contains(renderWPSubdirVhost(t, vd), "location /shop/") {
		t.Error("static domain must not render a WordPress route")
	}

	// Custom routing owns `location /`: render nothing of ours either.
	vd = base()
	vd.RootOverridden = true
	vd.WPSubdirRoutes = []string{"/shop"}
	if strings.Contains(renderWPSubdirVhost(t, vd), "location /shop/") {
		t.Error("RootOverridden vhost must not render a WordPress route")
	}

	// No subfolder installs: the vhost is unchanged.
	if a, b := renderWPSubdirVhost(t, base()), renderWPSubdirVhost(t, func() vhostData { v := base(); v.WPSubdirRoutes = []string{}; return v }()); a != b || strings.Contains(a, "WordPress in the") {
		t.Error("no routes must render the vhost unchanged")
	}
}
