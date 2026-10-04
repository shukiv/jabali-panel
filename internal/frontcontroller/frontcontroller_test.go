package frontcontroller

import "testing"

func TestValidateAcceptsRouterSetups(t *testing.T) {
	for _, c := range []struct{ script, query string }{
		{"/index.php", "$query_string"},             // the panel default
		{"/index.php", "mod=$uri&$args"},            // GH #1999 (Plesk setup)
		{"/index.php", "q=$uri&$args"},              // Drupal 7 style
		{"/public/index.php", "route=$request_uri"}, // subfolder front controller
		{"/app.php", ""},                            // no query string at all
		{"/index.php", "_url=$uri$is_args$args"},    // Phalcon style
		{"/index.php", "path=%2F$document_uri"},
	} {
		if err := Validate(c.script, c.query); err != nil {
			t.Errorf("Validate(%q, %q) = %v, want ok", c.script, c.query, err)
		}
	}
}

func TestValidateRejectsUnsafeOrBrokenValues(t *testing.T) {
	for _, c := range []struct{ script, query, why string }{
		{"", "$args", "no script"},
		{"index.php", "$args", "not from the site root"},
		{"/index.html", "$args", "not a .php script"},
		{"/../etc/passwd.php", "", "parent directory"},
		{"//evil.php", "", "double slash"},
		{"/a b.php", "", "space in script"},
		{"/index.php", "a=1; deny all", "semicolon and space"},
		{"/index.php", "a=$uri}", "closing brace"},
		{"/index.php", "a={", "opening brace"},
		{"/index.php", `a="x"`, "quote"},
		{"/index.php", "a=1#x", "comment marker"},
		{"/index.php", "a=$urix", "unknown variable (no boundary after $uri)"},
		{"/index.php", "a=$host", "variable not on the list"},
		{"/index.php", "a=${uri}", "brace variable syntax"},
		{"/index.php", "a=$", "bare dollar"},
		{"/index.php", "a=1\nb=2", "newline"},
		{"/index.php", "a=1 b", "non-breaking space"},
		{"/index.php", "a=\\x", "backslash"},
		{"/index.php", "a=1?b=2", "second question mark"},
	} {
		if err := Validate(c.script, c.query); err == nil {
			t.Errorf("Validate(%q, %q) accepted (%s)", c.script, c.query, c.why)
		}
	}
}

func TestFallbackAndParseRoundTrip(t *testing.T) {
	if Default != "/index.php?$query_string" {
		t.Fatalf("Default = %q; a PHP vhost without a rule must render exactly as before", Default)
	}
	if got := Fallback("/app.php", ""); got != "/app.php" {
		t.Errorf("Fallback without a query = %q", got)
	}
	s, q, err := Parse("/index.php?mod=$uri&$args")
	if err != nil || s != "/index.php" || q != "mod=$uri&$args" {
		t.Errorf("Parse = %q %q %v", s, q, err)
	}
	if _, _, err := Parse("/index.php?a=1;return 302 http://x"); err == nil {
		t.Error("Parse accepted an injected directive")
	}
}
