package api

import "testing"

// GH #1580: an admin authoring nginx_custom_directives gets the relaxed
// denylist validator (ValidateNginxDirectivesAdmin) — the full directive range,
// including proxy_pass and the reverse-proxy family the reporter needs, minus a
// small set of directives nginx -t can't catch. The strict allowlist validator
// (ValidateNginxDirectives) is unchanged for the tenant/default path.

func TestValidateNginxDirectivesAdmin_AllowsReverseProxy(t *testing.T) {
	// The reporter's exact reverse-proxy block — every line must pass now.
	ok := []string{
		"proxy_pass $arg_url;",
		"proxy_http_version 1.1;",
		`proxy_set_header Connection "";`,
		"proxy_buffering off;",
		"proxy_request_buffering off;",
		"proxy_ignore_client_abort on;",
		"proxy_redirect off;",
		"proxy_pass_request_headers on;",
		"proxy_set_header Host $proxy_host;",
		"proxy_read_timeout 3600s;",
		"proxy_connect_timeout 5s;",
		// A whole reverse-proxy location block (the RootOverridden flow).
		"location / {\n  proxy_pass http://127.0.0.1:9000;\n  proxy_set_header Host $host;\n}",
		// access_log / auth_basic with a real value (NOT the off form) are fine.
		"access_log /var/log/nginx/custom.log;",
		`auth_basic "Members Only";`,
	}
	for _, in := range ok {
		if msg := ValidateNginxDirectivesAdmin(in); msg != "" {
			t.Errorf("admin validator rejected a legitimate directive %q: %s", in, msg)
		}
	}
	// And these were exactly what the strict tenant validator blocked — proving
	// the two paths now differ (the whole point of #1580).
	if msg := ValidateNginxDirectives("proxy_pass http://x;"); msg == "" {
		t.Error("strict validator must STILL reject proxy_pass (tenant path unchanged)")
	}
}

func TestValidateNginxDirectivesAdmin_BlocksFootguns(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		// File disclosure — serve arbitrary paths over HTTP.
		{"root", "root /etc/jabali-panel;"},
		{"alias", "alias /etc/;"},
		// Arbitrary config/file inclusion.
		{"include", "include /etc/nginx/other-tenant.conf;"},
		// Arbitrary-file read / existence oracle.
		{"auth_basic_user_file", "auth_basic_user_file /etc/shadow;"},
		// Main-context-only footgun.
		{"load_module", "load_module modules/ngx_http_x.so;"},
		// The "off" value forms — nginx -t accepts them; they suppress security
		// logging / strip inherited auth.
		{"access_log off", "access_log off;"},
		{"auth_basic off", "auth_basic off;"},
		// Case + whitespace must not slip a footgun past the check.
		{"ROOT upper", "ROOT /etc;"},
		{"access_log   off tabs", "access_log\toff;"},
		// Structural guards still apply for admin.
		{"null byte", "add_header X \x00;"},
		{"unbalanced open", "location / {"},
		// Break-out attempt: a leading } would close the server block early.
		{"server-block escape", "}\nserver { listen 80; }\nlocation / {"},
		{"nesting too deep", "a { b { c { d { deny all; } } } }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := ValidateNginxDirectivesAdmin(tc.in); msg == "" {
				t.Errorf("admin validator accepted a footgun it must block: %q", tc.in)
			}
		})
	}
}
